package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/web/templates/pages"
)

// formUserID reads the Telegram user ID in the form's id field.
func formUserID(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%q isn't a Telegram user ID. It's a number like 123456789, not your @username", raw)
	}
	return id, nil
}

// SetupUsersAllow serves POST /setup/users/allow. It lets a Telegram user
// use the bot and, when they have messaged it, tells them they're in.
func (a *App) SetupUsersAllow(w http.ResponseWriter, r *http.Request) error {
	id, err := formUserID(r)
	if err != nil {
		return a.fail(w, r, err.Error())
	}
	if a.Store == nil {
		return a.fail(w, r, errNoStore.Error())
	}
	if err := a.Store.Allow(r.Context(), id); err != nil {
		return err
	}
	// Telegram only lets a bot message someone who messaged it first, so
	// this fails quietly for an ID typed in before that.
	if bot, err := a.bot(r.Context()); err == nil {
		_, _ = bot.SendMessage(r.Context(), telegram.SendMessageParams{
			ChatID: id,
			Text:   "✅ You're in! Tell me what you need to do and when, like \"call the bank tomorrow at 10\", or a habit to build, like \"drink water every 2 hours\".",
		})
	}
	return a.back(w, r, pages.Flash{Message: fmt.Sprintf("Allowed %d. They can use the bot now.", id)})
}

// SetupUsersRemove serves POST /setup/users/remove. It stops a Telegram
// user from using the bot. Their tasks and habits stay.
func (a *App) SetupUsersRemove(w http.ResponseWriter, r *http.Request) error {
	id, err := formUserID(r)
	if err != nil {
		return a.fail(w, r, err.Error())
	}
	if a.Store == nil {
		return a.fail(w, r, errNoStore.Error())
	}
	if err := a.Store.Disallow(r.Context(), id); err != nil {
		return err
	}
	return a.back(w, r, pages.Flash{Message: fmt.Sprintf("Removed %d. The bot won't answer them anymore.", id)})
}

// SetupUsersDismiss serves POST /setup/users/dismiss. It dismisses an access
// request.
func (a *App) SetupUsersDismiss(w http.ResponseWriter, r *http.Request) error {
	id, err := formUserID(r)
	if err != nil {
		return a.fail(w, r, err.Error())
	}
	if a.Store == nil {
		return a.fail(w, r, errNoStore.Error())
	}
	if err := a.Store.DeleteAccessRequest(r.Context(), id); err != nil {
		return err
	}
	return a.back(w, r, pages.Flash{Message: "Request dismissed."})
}
