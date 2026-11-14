package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNoBot is returned when no bot was connected on the setup page yet.
var ErrNoBot = errors.New("no bot is connected yet: add one on the setup page")

// Bot is the Telegram bot the setup page connected. Its token lives here
// rather than in the environment, so the setup page can walk the owner
// through creating and connecting one.
type Bot struct {
	Token string `json:"token"`
	// ID, Username and Name are what Telegram's getMe returned for Token.
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	// WebhookSecret is sent by Telegram with every update, so the webhook
	// knows updates come from Telegram. It is generated with the token.
	WebhookSecret string    `json:"webhookSecret"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Bot returns the connected bot, or ErrNoBot.
func (s *Store) Bot(ctx context.Context) (Bot, error) {
	var bot Bot
	err := s.getJSON(ctx, s.key("bot"), &bot)
	if errors.Is(err, ErrNotFound) || (err == nil && bot.Token == "") {
		return Bot{}, ErrNoBot
	}
	return bot, err
}

// SaveBot saves the connected bot. A bot saved for the first time, or with a
// new token, gets a new webhook secret.
func (s *Store) SaveBot(ctx context.Context, bot Bot) error {
	if bot.WebhookSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		// Telegram allows A-Z, a-z, 0-9, _ and -: the URL alphabet.
		bot.WebhookSecret = base64.RawURLEncoding.EncodeToString(secret)
	}
	return s.setJSON(ctx, s.key("bot"), bot)
}

// DeleteBot forgets the connected bot.
func (s *Store) DeleteBot(ctx context.Context) error {
	return s.rdb.Del(ctx, s.key("bot")).Err()
}

// AllowedUsers returns the Telegram user IDs allowed to use the bot.
func (s *Store) AllowedUsers(ctx context.Context) ([]int64, error) {
	members, err := s.rdb.SMembers(ctx, s.key("allowed")).Result()
	if err != nil {
		return nil, err
	}
	return parseIDs(members)
}

// Allowed reports whether a Telegram user may use the bot.
func (s *Store) Allowed(ctx context.Context, userID int64) (bool, error) {
	return s.rdb.SIsMember(ctx, s.key("allowed"), userID).Result()
}

// Allow lets a Telegram user use the bot, and drops their access request.
func (s *Store) Allow(ctx context.Context, userID int64) error {
	_, err := s.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.SAdd(ctx, s.key("allowed"), userID)
		pipe.HDel(ctx, s.key("requests"), strconv.FormatInt(userID, 10))
		return nil
	})
	return err
}

// Disallow stops a Telegram user from using the bot.
func (s *Store) Disallow(ctx context.Context, userID int64) error {
	return s.rdb.SRem(ctx, s.key("allowed"), userID).Err()
}

// AccessRequest is someone who messaged the bot without being allowed to,
// so the owner can allow them from the setup page with a tap.
type AccessRequest struct {
	ID        int64     `json:"id"`
	FirstName string    `json:"firstName"`
	LastName  string    `json:"lastName,omitempty"`
	Username  string    `json:"username,omitempty"`
	At        time.Time `json:"at"`
}

// maxRequests bounds the access requests kept, so strangers messaging the
// bot can't fill the store.
const maxRequests = 20

// RequestAccess records an access request, replacing an older one from the
// same user. Past maxRequests, the oldest are dropped.
func (s *Store) RequestAccess(ctx context.Context, request AccessRequest) error {
	if err := s.hashSet(ctx, s.key("requests"), strconv.FormatInt(request.ID, 10), request); err != nil {
		return err
	}
	requests, err := s.AccessRequests(ctx)
	if err != nil || len(requests) <= maxRequests {
		return err
	}
	for _, old := range requests[maxRequests:] {
		if err := s.DeleteAccessRequest(ctx, old.ID); err != nil {
			return err
		}
	}
	return nil
}

// AccessRequests returns the access requests, newest first.
func (s *Store) AccessRequests(ctx context.Context) ([]AccessRequest, error) {
	requests, err := hashValues[AccessRequest](ctx, s, s.key("requests"))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(requests, func(a, b AccessRequest) int { return b.At.Compare(a.At) })
	return requests, nil
}

// DeleteAccessRequest dismisses an access request.
func (s *Store) DeleteAccessRequest(ctx context.Context, userID int64) error {
	return s.rdb.HDel(ctx, s.key("requests"), strconv.FormatInt(userID, 10)).Err()
}
