package web

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/web/templates/pages"
)

// tokenPattern is the shape of a bot token from @BotFather: the bot's ID, a
// colon, and its secret.
var tokenPattern = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{30,}$`)

// commands is the bot's command menu.
var commands = []telegram.BotCommand{
	{Command: "today", Description: "Today's tasks and habits"},
	{Command: "tasks", Description: "All open tasks"},
	{Command: "habits", Description: "Your habits and streaks"},
	{Command: "pause", Description: "Pause habit reminders"},
	{Command: "resume", Description: "Resume habit reminders"},
	{Command: "id", Description: "Show your Telegram user ID"},
	{Command: "reset", Description: "Clear our chat (I keep your tasks)"},
}

// SetupToken serves POST /setup/token. It checks the token from @BotFather
// with Telegram, saves it with a new webhook secret, and sets the bot's
// command menu. A bot replaced by another stops getting webhook deliveries.
// A bot whose webhook is already set, as after its saved secret was lost,
// keeps its webhook with the new secret, which Telegram would otherwise
// keep sending the old one of.
func (a *App) SetupToken(w http.ResponseWriter, r *http.Request) error {
	if a.Store == nil {
		return a.fail(w, r, errNoStore.Error())
	}
	ctx := r.Context()
	token := strings.TrimSpace(r.FormValue("token"))
	if !tokenPattern.MatchString(token) {
		return a.fail(w, r, "That doesn't look like a bot token. It's the line in BotFather's reply that looks like 123456789:AAH4k…, with no spaces.")
	}
	client := telegram.New(token)
	me, err := client.GetMe(ctx)
	if err != nil {
		return a.fail(w, r, describeTelegramError(err))
	}

	old, err := a.Store.Bot(ctx)
	if err != nil && !errors.Is(err, store.ErrNoBot) {
		return err
	}
	bot := store.Bot{Token: token, ID: me.ID, Username: me.Username, Name: me.FirstName, UpdatedAt: time.Now()}
	if old.Token == token {
		bot.WebhookSecret = old.WebhookSecret
	} else if old.Token != "" {
		// Best effort: the old bot may have been revoked.
		_ = telegram.New(old.Token).DeleteWebhook(ctx)
	}
	if err := a.Store.SaveBot(ctx, bot); err != nil {
		return err
	}
	if saved, err := a.Store.Bot(ctx); err == nil && saved.WebhookSecret != old.WebhookSecret && !a.Polling {
		if info, err := client.GetWebhookInfo(ctx); err == nil && info.URL != "" {
			if err := client.SetWebhook(ctx, info.URL, saved.WebhookSecret); err != nil {
				return a.fail(w, r, "The token is saved, but reconnecting its webhook failed: "+err.Error())
			}
		}
	}
	if err := client.SetMyCommands(ctx, commands); err != nil {
		return a.fail(w, r, "The token is saved, but setting the bot's commands failed: "+err.Error())
	}
	return a.back(w, r, pages.Flash{Message: "Found your bot, @" + me.Username + " 🎉"})
}

// SetupReset serves POST /setup/reset. It disconnects the bot and forgets
// its token, so the guide starts over with a new one. Tasks, habits and
// allowed users stay.
func (a *App) SetupReset(w http.ResponseWriter, r *http.Request) error {
	if a.Store == nil {
		return a.fail(w, r, errNoStore.Error())
	}
	if bot, err := a.bot(r.Context()); err == nil {
		_ = bot.DeleteWebhook(r.Context())
	}
	if err := a.Store.DeleteBot(r.Context()); err != nil {
		return err
	}
	return a.redirectWithFlash(w, r, "/", pages.Flash{Message: "The bot is removed. Paste a new token to connect another one."})
}
