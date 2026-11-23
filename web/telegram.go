package web

import (
	"cmp"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/httpx"
	"github.com/eriicafes/httpx/httperrors"

	"github.com/eriicafes/workflow-taskmanager/lib/agenda"
	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

// TelegramWebhookPath is the path Telegram delivers updates to.
const TelegramWebhookPath = "/telegram"

// Telegram serves POST /telegram, Telegram's webhook. It answers as soon as
// it has handled the update, since Telegram retries slow webhooks. A failed
// update answers 500, so Telegram redelivers it, and redeliveries of handled
// updates are deduplicated.
func (a *App) Telegram(w http.ResponseWriter, r *http.Request) error {
	if a.Store == nil {
		return httperrors.New("unauthorized", http.StatusUnauthorized)
	}
	bot, err := a.Store.Bot(r.Context())
	if err != nil || bot.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(bot.WebhookSecret)) != 1 {
		return httperrors.New("unauthorized", http.StatusUnauthorized)
	}
	update, err := httpx.Read[telegram.Update](r)
	if err != nil {
		return httperrors.Wrap(err, "invalid update", http.StatusBadRequest)
	}
	if err := a.HandleUpdate(r.Context(), update); err != nil {
		return httpx.InternalError(fmt.Errorf("telegram update %d: %w", update.UpdateID, err), "failed")
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

// HandleUpdate handles a Telegram update: a message goes to the user's
// Conversation run, which answers their mailbox in order, unless it is a
// command answered right away; a button tap acts on a reminder.
func (a *App) HandleUpdate(ctx context.Context, update telegram.Update) error {
	if a.Store == nil {
		return errNoStore
	}
	ctx = workflow.NewContext(ctx, a.Client)
	key := "update:" + strconv.FormatInt(update.UpdateID, 10)
	if first, err := a.Store.Once(ctx, key, 24*time.Hour); err != nil || !first {
		return err
	}
	bot, err := a.bot(ctx)
	if err == nil {
		switch {
		case update.CallbackQuery != nil:
			err = a.handleCallback(ctx, bot, *update.CallbackQuery)
		case update.Message != nil:
			err = a.handleMessage(ctx, bot, *update.Message)
		}
	}
	if err != nil {
		return errors.Join(err, a.Store.Release(ctx, key))
	}
	return nil
}

func (a *App) handleMessage(ctx context.Context, bot *telegram.Client, message telegram.Message) error {
	chatID := message.Chat.ID
	if message.Chat.Type != "private" || message.From == nil {
		return nil
	}
	send := func(text string) error {
		_, err := bot.SendMessage(ctx, telegram.SendMessageParams{ChatID: chatID, Text: text})
		return err
	}
	text := strings.TrimSpace(cmp.Or(message.Text, message.Caption))
	command, _, _ := strings.Cut(text, " ")
	command, _, _ = strings.Cut(command, "@")
	if command == "/id" {
		return send(fmt.Sprintf("Your Telegram user ID is %d", message.From.ID))
	}

	allowed, err := a.Store.Allowed(ctx, message.From.ID)
	if err != nil {
		return err
	}
	if !allowed {
		// The setup guide shows the request, so the owner can allow
		// themselves with a tap.
		if err := a.Store.RequestAccess(ctx, store.AccessRequest{
			ID:        message.From.ID,
			FirstName: message.From.FirstName,
			LastName:  message.From.LastName,
			Username:  message.From.Username,
			At:        time.Now(),
		}); err != nil {
			return err
		}
		return send(fmt.Sprintf("👋 Hi %s! Your Telegram user ID is %d\n\nThis bot is private. If it's yours, open its setup page and press Allow next to your name, or paste this ID there.", message.From.FirstName, message.From.ID))
	}

	user, _, err := a.Store.EnsureUser(ctx, chatID, message.From.FirstName, time.Now())
	if err != nil {
		return err
	}
	if err := a.EnsureAgenda(ctx, chatID); err != nil {
		log.Printf("agenda for %d: %v", chatID, err)
	}

	now := time.Now().In(user.Location())
	switch command {
	case "":
		return send("I can only read text for now. Tell me what you need to do 📝")
	case "/reset":
		if err := a.Store.ClearHistory(ctx, chatID); err != nil {
			return err
		}
		return send("Fresh start! I cleared our chat, but your tasks and habits are all still here ✅")
	case "/today":
		tasks, habits, err := a.lists(ctx, chatID)
		if err != nil {
			return err
		}
		return send(cmp.Or(agenda.Today(tasks, habits, now), "Nothing on today 🌤️ Tell me what's coming up."))
	case "/tasks":
		tasks, _, err := a.lists(ctx, chatID)
		if err != nil {
			return err
		}
		return send(agenda.Tasks(tasks, now))
	case "/habits":
		_, habits, err := a.lists(ctx, chatID)
		if err != nil {
			return err
		}
		return send(agenda.Habits(habits, now))
	case "/pause", "/resume":
		user.Paused = command == "/pause"
		if err := a.Store.SaveUser(ctx, user); err != nil {
			return err
		}
		if user.Paused {
			return send("⏸️ Habit reminders and your morning agenda are paused. Task reminders still come through. Send /resume when you're ready.")
		}
		return send("▶️ Habit reminders are back on.")
	case "/start":
		text = "Hi! I just started chatting with you. Introduce yourself briefly and ask where I'm based."
	}

	_ = bot.SendChatAction(ctx, chatID, "typing")
	return workflows.Deliver(ctx, a.Store, chatID, store.Mail{Text: text})
}

func (a *App) lists(ctx context.Context, chatID int64) ([]store.Task, []store.Habit, error) {
	tasks, err := a.Store.Tasks(ctx, chatID)
	if err != nil {
		return nil, nil, err
	}
	habits, err := a.Store.Habits(ctx, chatID)
	return tasks, habits, err
}

// handleCallback handles a tap on a reminder's button, whose data is
// "<action>:<id>": td marks a task done, tz snoozes it an hour, hd marks a
// habit done and hs skips it. The reminder's message is edited to show what
// happened, and the agent gets a note so it knows.
func (a *App) handleCallback(ctx context.Context, bot *telegram.Client, query telegram.CallbackQuery) error {
	action, id, ok := strings.Cut(query.Data, ":")
	if query.Message == nil || !ok {
		return bot.AnswerCallbackQuery(ctx, query.ID, "")
	}
	chatID, messageID := query.Message.Chat.ID, query.Message.MessageID
	if allowed, err := a.Store.Allowed(ctx, query.From.ID); err != nil || !allowed {
		return errors.Join(err, bot.AnswerCallbackQuery(ctx, query.ID, "This bot is private"))
	}
	user, err := a.Store.User(ctx, chatID)
	if err != nil {
		return err
	}
	now := time.Now().In(user.Location())

	var answer, text, note string
	switch action {
	case "td", "tz":
		task, err := a.Store.Task(ctx, chatID, id)
		if errors.Is(err, store.ErrNotFound) {
			answer, text = "That task is gone", "🗑️ Deleted task"
			break
		} else if err != nil {
			return err
		}
		if action == "td" {
			if !task.Done {
				task.Done, task.DoneAt = true, now
			}
			answer, text = "Nice work! 🎉", "✅ "+task.Title
			note = fmt.Sprintf("The user marked task %s %q done from its reminder.", task.ID, task.Title)
		} else {
			task.RemindAt = now.Add(time.Hour).Truncate(time.Minute)
			task.Version++
			answer = "I'll remind you in an hour"
			text = fmt.Sprintf("⏰ %s\nSnoozed until %s", task.Title, task.RemindAt.Format(agenda.Clock))
			note = fmt.Sprintf("The user snoozed task %s %q to %s.", task.ID, task.Title, task.RemindAt.Format(agenda.Clock))
		}
		if err := a.Store.SaveTask(ctx, chatID, task); err != nil {
			return err
		}
		if action == "tz" {
			ctx := workflow.NewContext(ctx, a.Client)
			if _, err := workflows.StartOnLatest(ctx, workflows.Reminder{ChatID: chatID, Kind: workflows.KindTask, ItemID: task.ID, At: task.RemindAt, Version: task.Version}); err != nil {
				return err
			}
		} else if err := a.Store.PruneTasks(ctx, chatID); err != nil {
			return err
		}
	case "hd":
		habit, err := a.Store.LogHabit(ctx, chatID, id, now)
		if errors.Is(err, store.ErrNotFound) {
			answer, text = "That habit is gone", "🗑️ Deleted habit"
			break
		} else if err != nil {
			return err
		}
		answer, text = "Done ✅", "✅ "+habit.Name
		if habit.Streak > 1 {
			text += fmt.Sprintf("\n🔥 %d day streak", habit.Streak)
			answer = fmt.Sprintf("🔥 %d day streak!", habit.Streak)
		}
		note = fmt.Sprintf("The user marked habit %s %q done from its reminder; streak %d days.", habit.ID, habit.Name, habit.Streak)
	case "hs":
		habit, err := a.Store.Habit(ctx, chatID, id)
		if errors.Is(err, store.ErrNotFound) {
			answer, text = "That habit is gone", "🗑️ Deleted habit"
			break
		} else if err != nil {
			return err
		}
		answer, text = "Skipped", "⏭️ "+habit.Name+" skipped"
		note = fmt.Sprintf("The user skipped habit %s %q from its reminder.", habit.ID, habit.Name)
	default:
		return bot.AnswerCallbackQuery(ctx, query.ID, "")
	}
	_ = bot.AnswerCallbackQuery(ctx, query.ID, answer)
	_ = bot.EditMessageText(ctx, chatID, messageID, text)
	if note == "" {
		return nil
	}
	return a.Store.Note(ctx, chatID, note)
}
