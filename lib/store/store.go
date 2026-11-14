// Package store keeps Task Manager's data in Redis: the bot the setup page
// connected and who may use it, and for each user their settings, tasks,
// habits and conversation history.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/eriicafes/go-workflow/workflowai"
	"github.com/redis/go-redis/v9"
)

// HistoryLimit is how many recent messages of a conversation are sent to the
// model. Older ones are folded into the conversation's Summary.
const HistoryLimit = 60

// ErrNotFound is returned when a user, task or habit does not exist.
var ErrNotFound = errors.New("not found")

// User is someone chatting with the bot.
type User struct {
	ChatID   int64  `json:"chatId"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	// AgendaHour is the local hour the day's agenda arrives, or -1 for none.
	AgendaHour int `json:"agendaHour"`
	// Paused stops habit reminders and the agenda. Task reminders still fire.
	Paused        bool      `json:"paused"`
	CreatedAt     time.Time `json:"createdAt"`
	LastMessageAt time.Time `json:"lastMessageAt,omitzero"`
}

// Location returns the user's time zone, or UTC when it is unknown.
func (u User) Location() *time.Location {
	if loc, err := time.LoadLocation(u.Timezone); err == nil && u.Timezone != "" {
		return loc
	}
	return time.UTC
}

// Store reads and writes Task Manager's data.
type Store struct {
	rdb    redis.UniversalClient
	prefix string
}

// New returns a Store on rdb whose keys start with prefix.
func New(rdb redis.UniversalClient, prefix string) *Store {
	return &Store{rdb: rdb, prefix: prefix}
}

// Open returns the Store on REDIS_URL, the Upstash Redis connection string,
// with keys under taskmanager.
func Open() (*Store, error) {
	options, err := redis.ParseURL(os.Getenv("REDIS_URL"))
	if err != nil {
		return nil, fmt.Errorf("REDIS_URL: %w", err)
	}
	return New(redis.NewClient(options), "taskmanager"), nil
}

func (s *Store) key(parts ...any) string {
	var b strings.Builder
	b.WriteString(s.prefix)
	for _, part := range parts {
		fmt.Fprintf(&b, ":%v", part)
	}
	return b.String()
}

// EnsureUser returns the user of chatID, creating it with defaults when it is
// new, and reports whether it was created. It records now as the time of the
// user's last message.
func (s *Store) EnsureUser(ctx context.Context, chatID int64, name string, now time.Time) (User, bool, error) {
	user := User{ChatID: chatID, Name: name, Timezone: "UTC", AgendaHour: 8, CreatedAt: now, LastMessageAt: now}
	data, err := json.Marshal(user)
	if err != nil {
		return User{}, false, err
	}
	created, err := s.rdb.SetNX(ctx, s.key("user", chatID), data, 0).Result()
	if err != nil {
		return User{}, false, err
	}
	if created {
		return user, true, s.rdb.SAdd(ctx, s.key("users"), chatID).Err()
	}
	user, err = s.User(ctx, chatID)
	if err != nil {
		return User{}, false, err
	}
	user.LastMessageAt = now
	return user, false, s.SaveUser(ctx, user)
}

// User returns the user of chatID.
func (s *Store) User(ctx context.Context, chatID int64) (User, error) {
	var user User
	return user, s.getJSON(ctx, s.key("user", chatID), &user)
}

// SaveUser replaces a user.
func (s *Store) SaveUser(ctx context.Context, user User) error {
	return s.setJSON(ctx, s.key("user", user.ChatID), user)
}

// Users returns the chat IDs of every user.
func (s *Store) Users(ctx context.Context) ([]int64, error) {
	members, err := s.rdb.SMembers(ctx, s.key("users")).Result()
	if err != nil {
		return nil, err
	}
	return parseIDs(members)
}

// AgendaRun returns the ID of the run that carries the user's daily agenda,
// or "" when none was started.
func (s *Store) AgendaRun(ctx context.Context, chatID int64) (string, error) {
	id, err := s.rdb.Get(ctx, s.key("agenda", chatID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return id, err
}

// SetAgendaRun records the run that carries the user's daily agenda.
func (s *Store) SetAgendaRun(ctx context.Context, chatID int64, runID string) error {
	return s.rdb.Set(ctx, s.key("agenda", chatID), runID, 0).Err()
}

// ContinueAgendaRun records next as the run that carries the user's agenda
// when prev still does, and reports whether it did. A chain that was
// replaced meanwhile doesn't take the agenda back.
func (s *Store) ContinueAgendaRun(ctx context.Context, chatID int64, prev, next string) (bool, error) {
	key := s.key("agenda", chatID)
	continued := false
	err := s.rdb.Watch(ctx, func(tx *redis.Tx) error {
		current, err := tx.Get(ctx, key).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if current != prev {
			return nil
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, next, 0)
			return nil
		})
		continued = err == nil
		return err
	}, key)
	if errors.Is(err, redis.TxFailedErr) {
		return false, nil
	}
	return continued, err
}

// nextID returns a new short ID for one of the user's tasks or habits, such
// as "t4" or "h2": short IDs are easy for the model to repeat.
func (s *Store) nextID(ctx context.Context, chatID int64, kind string) (string, error) {
	n, err := s.rdb.Incr(ctx, s.key("seq", kind, chatID)).Result()
	if err != nil {
		return "", err
	}
	return kind + strconv.FormatInt(n, 10), nil
}

// History returns the end of the user's conversation. It starts at a user
// message, so it never begins in the middle of a tool call.
func (s *Store) History(ctx context.Context, chatID int64) ([]workflowai.ModelMessage, error) {
	values, err := s.rdb.LRange(ctx, s.key("history", chatID), -HistoryLimit, -1).Result()
	if err != nil {
		return nil, err
	}
	messages, err := decodeMessages(values)
	if err != nil {
		return nil, err
	}
	start := slices.IndexFunc(messages, func(m workflowai.ModelMessage) bool { return m.Role == workflowai.RoleUser })
	if start < 0 {
		return nil, nil
	}
	return messages[start:], nil
}

// FullHistory returns every stored message of the user's conversation,
// oldest first.
func (s *Store) FullHistory(ctx context.Context, chatID int64) ([]workflowai.ModelMessage, error) {
	values, err := s.rdb.LRange(ctx, s.key("history", chatID), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	return decodeMessages(values)
}

func decodeMessages(values []string) ([]workflowai.ModelMessage, error) {
	messages := make([]workflowai.ModelMessage, 0, len(values))
	for _, value := range values {
		var message workflowai.ModelMessage
		if err := json.Unmarshal([]byte(value), &message); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// AppendHistory adds messages to the end of the user's conversation in one
// write, so the messages of a turn stay together, and trims old messages.
func (s *Store) AppendHistory(ctx context.Context, chatID int64, messages []workflowai.ModelMessage) error {
	if len(messages) == 0 {
		return nil
	}
	values := make([]any, len(messages))
	for i, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			return err
		}
		values[i] = data
	}
	key := s.key("history", chatID)
	_, err := s.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.RPush(ctx, key, values...)
		pipe.LTrim(ctx, key, -2*HistoryLimit, -1)
		return nil
	})
	return err
}

// Note adds a message to the user's conversation that tells the agent about
// something that happened outside it, such as a reminder sent or a button
// tapped, so it has that context when the user next writes.
func (s *Store) Note(ctx context.Context, chatID int64, text string) error {
	return s.AppendHistory(ctx, chatID, []workflowai.ModelMessage{{
		Role:    workflowai.RoleUser,
		Content: []workflowai.ContentPart{{Type: workflowai.PartText, Text: NotePrefix + text}},
	}})
}

// NotePrefix starts every message Task Manager adds to a conversation
// itself, so the agent and the summarizer can tell it from the user's own.
const NotePrefix = "[Task Manager, not typed by the user] "

// ClearHistory forgets the user's conversation and its summary.
func (s *Store) ClearHistory(ctx context.Context, chatID int64) error {
	return s.rdb.Del(ctx, s.key("history", chatID), s.key("summary", chatID)).Err()
}

// Summary is the running summary of the older part of a conversation,
// whose messages were folded out of the history.
type Summary struct {
	Text      string    `json:"text"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Summary returns the user's conversation summary, which is empty until the
// history is first compacted.
func (s *Store) Summary(ctx context.Context, chatID int64) (Summary, error) {
	var summary Summary
	err := s.getJSON(ctx, s.key("summary", chatID), &summary)
	if errors.Is(err, ErrNotFound) {
		return Summary{}, nil
	}
	return summary, err
}

// CompactHistory replaces the first n messages of the user's conversation
// with summary, which must cover them. first is the oldest of them as it was
// summarized: when another write has changed the start of the history since,
// nothing changes and CompactHistory reports false.
func (s *Store) CompactHistory(ctx context.Context, chatID int64, n int, first workflowai.ModelMessage, summary Summary) (bool, error) {
	key := s.key("history", chatID)
	data, err := json.Marshal(summary)
	if err != nil {
		return false, err
	}
	compacted := false
	err = s.rdb.Watch(ctx, func(tx *redis.Tx) error {
		value, err := tx.LIndex(ctx, key, 0).Result()
		if errors.Is(err, redis.Nil) {
			return nil
		} else if err != nil {
			return err
		}
		var current workflowai.ModelMessage
		if err := json.Unmarshal([]byte(value), &current); err != nil {
			return err
		}
		// Compare the decoded messages, since first was decoded too.
		if !reflect.DeepEqual(current, first) {
			return nil
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, s.key("summary", chatID), data, 0)
			pipe.LTrim(ctx, key, int64(n), -1)
			return nil
		})
		compacted = err == nil
		return err
	}, key)
	if errors.Is(err, redis.TxFailedErr) {
		return false, nil
	}
	return compacted, err
}

// Mail is a message the user sent, waiting in their mailbox for their
// conversation run.
type Mail struct {
	Text string `json:"text"`
}

// MailBatch is the most mail PeekMail returns at once.
const MailBatch = 20

// PushMail adds mail to the end of the user's mailbox.
func (s *Store) PushMail(ctx context.Context, chatID int64, mail Mail) error {
	data, err := json.Marshal(mail)
	if err != nil {
		return err
	}
	return s.rdb.RPush(ctx, s.key("mailbox", chatID), data).Err()
}

// PeekMail returns up to MailBatch of the oldest mail in the user's mailbox
// without removing it. AckMail removes it once it is handled.
func (s *Store) PeekMail(ctx context.Context, chatID int64) ([]Mail, error) {
	values, err := s.rdb.LRange(ctx, s.key("mailbox", chatID), 0, MailBatch-1).Result()
	if err != nil {
		return nil, err
	}
	mails := make([]Mail, len(values))
	for i, value := range values {
		if err := json.Unmarshal([]byte(value), &mails[i]); err != nil {
			return nil, err
		}
	}
	return mails, nil
}

// AckMail removes the n oldest mail from the user's mailbox. Mail is only
// added at the end, so these are the ones PeekMail returned.
func (s *Store) AckMail(ctx context.Context, chatID int64, n int) error {
	return s.rdb.LTrim(ctx, s.key("mailbox", chatID), int64(n), -1).Err()
}

// MailCount returns how much mail waits in the user's mailbox.
func (s *Store) MailCount(ctx context.Context, chatID int64) (int64, error) {
	return s.rdb.LLen(ctx, s.key("mailbox", chatID)).Result()
}

// Once reports whether key is claimed for the first time within ttl. It
// deduplicates Telegram updates and the start of agenda chains.
func (s *Store) Once(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return s.rdb.SetNX(ctx, s.key("once", key), 1, ttl).Result()
}

// Release releases a key claimed with Once, so a failed attempt can be
// retried.
func (s *Store) Release(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, s.key("once", key)).Err()
}

func (s *Store) getJSON(ctx context.Context, key string, v any) error {
	value, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	} else if err != nil {
		return err
	}
	return json.Unmarshal([]byte(value), v)
}

func (s *Store) setJSON(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, key, data, 0).Err()
}

// hashValues decodes every value of a hash.
func hashValues[T any](ctx context.Context, s *Store, key string) ([]T, error) {
	values, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	items := make([]T, 0, len(values))
	for _, value := range values {
		var item T
		if err := json.Unmarshal([]byte(value), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// hashValue decodes one value of a hash, or returns ErrNotFound.
func hashValue[T any](ctx context.Context, s *Store, key, field, what string) (T, error) {
	var item T
	value, err := s.rdb.HGet(ctx, key, field).Result()
	if errors.Is(err, redis.Nil) {
		return item, fmt.Errorf("%w: no %s %q", ErrNotFound, what, field)
	} else if err != nil {
		return item, err
	}
	return item, json.Unmarshal([]byte(value), &item)
}

func (s *Store) hashSet(ctx context.Context, key, field string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.rdb.HSet(ctx, key, field, data).Err()
}

func parseIDs(members []string) ([]int64, error) {
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		id, err := strconv.ParseInt(member, 10, 64)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}
