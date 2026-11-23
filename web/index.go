package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/eriicafes/workflow-taskmanager/lib/agenda"
	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/web/templates/pages"
)

// Index serves GET /. Until the bot is set up, or with ?guide=1, it is the
// setup guide, which walks through creating a bot, saving its token,
// connecting its webhook and allowing the owner's Telegram ID. Once set up,
// it is the dashboard: the bot's status, who may use it, and each user's
// tasks and habits.
func (a *App) Index(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	setup := a.setup(ctx, publicURL(r))
	flash, _ := a.Flash.Get(w, r)
	if !setup.Complete() || r.URL.Query().Has("guide") {
		return a.render(w, http.StatusOK, pages.Guide{Setup: setup, Flash: flash})
	}
	page := pages.Index{Setup: setup, Flash: flash}
	var err error
	if page.Users, err = a.users(ctx, time.Now()); err != nil {
		page.UsersError = err.Error()
	}
	return a.render(w, http.StatusOK, page)
}

// setup checks how far setup has come: the store, the saved bot, which
// Telegram is asked about, its webhook, and who may use it.
func (a *App) setup(ctx context.Context, publicURL string) pages.Setup {
	setup := pages.Setup{PublicURL: publicURL, WebhookURL: publicURL + TelegramWebhookPath, Polling: a.Polling}
	if a.Store == nil {
		setup.StoreError = "Set REDIS_URL so Task Manager can save your bot and your tasks."
		return setup
	}
	bot, err := a.Store.Bot(ctx)
	if errors.Is(err, store.ErrNoBot) {
		bot, err = store.Bot{}, nil
	}
	if err != nil {
		setup.StoreError = err.Error()
		return setup
	}
	if bot.Token != "" {
		setup.Bot = pages.Bot{Username: bot.Username, Name: bot.Name, TokenHint: tokenHint(bot.Token)}
		client := telegram.New(bot.Token)
		if _, err := client.GetMe(ctx); err != nil {
			setup.BotError = describeTelegramError(err)
		} else if !a.Polling {
			if info, err := client.GetWebhookInfo(ctx); err != nil {
				setup.BotError = describeTelegramError(err)
			} else {
				setup.Webhook = info
				setup.Connected = info.URL != "" && info.URL == setup.WebhookURL
				// Telegram reports its last error even once deliveries
				// recover, so one from before the last delivery is stale.
				delivered, _ := a.Store.Delivered(ctx)
				if info.LastErrorMessage != "" && info.LastErrorDate > delivered.Unix() {
					setup.LastError = info.LastErrorMessage + " (" + time.Unix(info.LastErrorDate, 0).UTC().Format("2 Jan 15:04 UTC") + ")"
				}
			}
		}
	}

	allowed, err := a.Store.AllowedUsers(ctx)
	if err != nil {
		setup.StoreError = err.Error()
		return setup
	}
	for _, id := range allowed {
		user := pages.AllowedUser{ID: id}
		if u, err := a.Store.User(ctx, id); err == nil {
			user.Name = u.Name
		}
		setup.Allowed = append(setup.Allowed, user)
	}
	requests, err := a.Store.AccessRequests(ctx)
	if err != nil {
		setup.StoreError = err.Error()
		return setup
	}
	for _, request := range requests {
		setup.Requests = append(setup.Requests, pages.Request{
			ID:       request.ID,
			Name:     strings.TrimSpace(request.FirstName + " " + request.LastName),
			Username: request.Username,
			When:     ago(request.At, time.Now()),
		})
	}
	return setup
}

// tokenHint shows the bot ID part of a token and hides its secret.
func tokenHint(token string) string {
	id, _, _ := strings.Cut(token, ":")
	return id + ":••••••••"
}

// describeTelegramError explains Telegram's answer to a bad token.
func describeTelegramError(err error) string {
	var tgErr *telegram.Error
	if errors.As(err, &tgErr) && (tgErr.Code == http.StatusUnauthorized || tgErr.Code == http.StatusNotFound) {
		return "Telegram doesn't recognize this token. It may have been revoked in @BotFather; paste the current one."
	}
	return err.Error()
}

// users returns every user, by name, with their open tasks and habits.
func (a *App) users(ctx context.Context, now time.Time) ([]pages.User, error) {
	if a.Store == nil {
		return nil, errNoStore
	}
	ids, err := a.Store.Users(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]pages.User, 0, len(ids))
	for _, id := range ids {
		user, err := a.Store.User(ctx, id)
		if err != nil {
			return nil, err
		}
		local := now.In(user.Location())
		entry := pages.User{Name: cmp.Or(user.Name, fmt.Sprintf("User %d", id)), Timezone: user.Timezone, Paused: user.Paused}
		tasks, err := a.Store.Tasks(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			if task.Done {
				entry.Done++
				continue
			}
			item := pages.Task{Title: task.Title, Overdue: agenda.Overdue(task, local)}
			if !task.Due.IsZero() {
				item.Due = agenda.When(task.Due, local, task.AllDay)
			}
			entry.Tasks = append(entry.Tasks, item)
		}
		habits, err := a.Store.Habits(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, habit := range habits {
			entry.Habits = append(entry.Habits, pages.Habit{
				Name:     habit.Name,
				Schedule: habit.Describe(),
				Streak:   habit.CurrentStreak(local),
				Today:    habit.DoneOn(local),
			})
		}
		result = append(result, entry)
	}
	slices.SortFunc(result, func(a, b pages.User) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return result, nil
}

// ago describes when at was, relative to now.
func ago(at, now time.Time) string {
	switch d := now.Sub(at); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// publicURL guesses the URL the bot is deployed at: the production domain
// on Vercel, and otherwise the URL of this request.
func publicURL(r *http.Request) string {
	if domain := os.Getenv("VERCEL_PROJECT_PRODUCTION_URL"); domain != "" {
		return "https://" + domain
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return cmp.Or(r.Header.Get("X-Forwarded-Proto"), scheme) + "://" + r.Host
}
