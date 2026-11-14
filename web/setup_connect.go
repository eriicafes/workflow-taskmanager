package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/eriicafes/workflow-taskmanager/web/templates/pages"
)

// SetupConnect serves POST /setup/connect. It points Telegram's webhook at
// the public URL in the form, with the saved webhook secret, and sets the
// bot's command menu.
func (a *App) SetupConnect(w http.ResponseWriter, r *http.Request) error {
	base, err := parsePublicURL(r.FormValue("public_url"))
	if err != nil {
		return a.fail(w, r, err.Error())
	}
	if a.Store == nil {
		return a.fail(w, r, errNoStore.Error())
	}
	saved, err := a.Store.Bot(r.Context())
	if err != nil {
		return a.fail(w, r, err.Error())
	}
	bot, err := a.bot(r.Context())
	if err != nil {
		return a.fail(w, r, err.Error())
	}
	if err := bot.SetWebhook(r.Context(), base+TelegramWebhookPath, saved.WebhookSecret); err != nil {
		return a.fail(w, r, err.Error())
	}
	if err := bot.SetMyCommands(r.Context(), commands); err != nil {
		return a.fail(w, r, "The webhook is connected, but setting commands failed: "+err.Error())
	}
	return a.back(w, r, pages.Flash{Message: "Connected! Telegram now delivers messages to " + base + TelegramWebhookPath})
}

func parsePublicURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("enter the full URL Task Manager is deployed at, like https://taskmanager.vercel.app")
	}
	if u.Scheme != "https" {
		return "", errors.New("Telegram only delivers to https URLs. Deploy first, or run locally with cmd/local, which needs no webhook")
	}
	return "https://" + u.Host + strings.TrimSuffix(u.Path, "/"), nil
}

// SetupDisconnect serves POST /setup/disconnect. It removes the webhook, so
// Telegram stops delivering messages.
func (a *App) SetupDisconnect(w http.ResponseWriter, r *http.Request) error {
	bot, err := a.bot(r.Context())
	if err != nil {
		return a.fail(w, r, err.Error())
	}
	if err := bot.DeleteWebhook(r.Context()); err != nil {
		return a.fail(w, r, err.Error())
	}
	return a.back(w, r, pages.Flash{Message: "Disconnected. The bot won't receive messages until you connect it again."})
}
