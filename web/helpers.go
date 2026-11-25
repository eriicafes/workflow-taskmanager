package web

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/worlds"
	"github.com/eriicafes/httpx/session"
	"github.com/eriicafes/tmpl"

	"github.com/eriicafes/workflow-taskmanager/lib/agenda"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/web/templates/pages"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

// Helpers shared by the route handlers.

var errNoStore = errors.New("REDIS_URL is not set")

// render renders a page as HTML.
func (a *App) render(w http.ResponseWriter, status int, page tmpl.Template) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return a.Templates.Render(w, page)
}

// redirectWithFlash sends the browser to path, showing flash once there.
func (a *App) redirectWithFlash(w http.ResponseWriter, r *http.Request, path string, flash pages.Flash) error {
	if err := a.Flash.Set(w, flash); err != nil {
		return err
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
	return nil
}

// back redirects to where a setup form was posted from: the guide or the
// dashboard.
func (a *App) back(w http.ResponseWriter, r *http.Request, flash pages.Flash) error {
	path := "/"
	if r.FormValue("from") == "guide" {
		path = "/?guide=1"
	}
	return a.redirectWithFlash(w, r, path, flash)
}

// fail redirects back with an error message.
func (a *App) fail(w http.ResponseWriter, r *http.Request, message string) error {
	return a.back(w, r, pages.Flash{Message: message, Error: true})
}

// bot returns a client for the connected bot.
func (a *App) bot(ctx context.Context) (*telegram.Client, error) {
	if a.Store == nil {
		return nil, errNoStore
	}
	return workflows.Bot(ctx, a.Store)
}

// cookies returns the session and flash cookies, signed with keys derived
// from password, so changing the password signs everyone out.
func cookies(password string, secure bool) (session.Cookie[Session], session.FlashCookie[pages.Flash], error) {
	sessionKey := sha256.Sum256([]byte("taskmanager session " + password))
	flashKey := sha256.Sum256([]byte("taskmanager flash " + password))
	sessionCookie, err := session.NewCookie[Session](session.DefaultCookieOptions("taskmanager_session",
		session.WithCookieSecret(session.Secret(sessionKey[:])),
		session.WithCookieSecure(secure),
		session.WithCookieDuration(SessionDuration),
	))
	if err != nil {
		return nil, nil, err
	}
	flashCookie, err := session.NewFlashCookie[pages.Flash](session.DefaultCookieOptions("taskmanager_flash",
		session.WithCookieSecret(session.Secret(flashKey[:])),
		session.WithCookieSecure(secure),
	))
	return sessionCookie, flashCookie, err
}

// EnsureAgenda starts a chain of Agenda runs for the user, unless the one
// they have is still going. Each run starts the next day's run, so this only
// starts a chain for a new user or restarts one that failed.
func (a *App) EnsureAgenda(ctx context.Context, chatID int64) error {
	if a.Store == nil {
		return errNoStore
	}
	ctx = workflow.NewContext(ctx, a.Client)
	id, err := a.Store.AgendaRun(ctx, chatID)
	if err != nil {
		return err
	}
	if id != "" {
		run, err := workflow.Get[workflows.AgendaResult](ctx, id)
		if err != nil {
			return err
		}
		status, err := run.Status(ctx)
		if err == nil && !status.Terminal() {
			return nil
		}
		if err != nil && !errors.Is(err, worlds.ErrRunNotFound) {
			return err
		}
	}
	// Two messages at once must not start two chains.
	key := fmt.Sprintf("agenda-start:%d", chatID)
	if first, err := a.Store.Once(ctx, key, time.Minute); err != nil || !first {
		return err
	}
	if err := a.startAgenda(ctx, chatID); err != nil {
		return errors.Join(err, a.Store.Release(ctx, key))
	}
	return nil
}

// startAgenda starts a new chain of Agenda runs for the user and records it
// as theirs. A chain it replaces ends when its run wakes and finds it is no
// longer the recorded one. ctx must carry the workflow Client.
func (a *App) startAgenda(ctx context.Context, chatID int64) error {
	user, err := a.Store.User(ctx, chatID)
	if err != nil {
		return err
	}
	run, err := workflow.Start(ctx, workflows.Agenda{ChatID: chatID, At: agenda.Next(user.AgendaHour, user.Location(), time.Now())})
	if err != nil {
		return err
	}
	return a.Store.SetAgendaRun(ctx, chatID, run.ID())
}
