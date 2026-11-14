// Package web serves Task Manager's HTTP routes through App: Telegram's
// webhook, and the setup page that guides the owner through creating a bot,
// connecting it, and allowing themselves to use it, rendered with tmpl and
// styled with Tailwind built by Vite. Each route lives in the file named
// after its path, and Handler collects them.
package web

import (
	"cmp"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/httpx"
	"github.com/eriicafes/httpx/httperrors"
	"github.com/eriicafes/httpx/session"
	"github.com/eriicafes/tmpl"
	"github.com/eriicafes/tmpl/vite"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/web/templates"
	"github.com/eriicafes/workflow-taskmanager/web/templates/pages"
)

// dist is the Vite build output, built with `pnpm build` at the root.
//
//go:embed all:dist
var dist embed.FS

// App serves Task Manager's routes with its dependencies.
type App struct {
	// Store is nil when REDIS_URL is not set, so the setup page still
	// shows what is missing.
	Store *store.Store
	// Client starts and reads runs. NewApp leaves it unset: the entry point
	// sets it on the World it runs with.
	Client    workflow.Client
	Vite      *vite.Vite
	Templates *tmpl.Templates
	// Session marks a browser signed in to the setup page, and Flash
	// carries a message across the redirect after a form posts. Both are
	// signed with keys derived from Password.
	Session session.Cookie[Session]
	Flash   session.FlashCookie[pages.Flash]
	// Password is SETUP_PASSWORD; an empty password disables sign in.
	Password string
	// Polling reports that updates come from long polling, as with
	// cmd/local, so the setup guide skips the webhook.
	Polling bool
	// Workflow serves the World's endpoints under /v1/workflow, such as the
	// RPC endpoint the Workflow SDK web UI reads runs through. Entry points
	// set it from their World, protected as their network needs.
	Workflow http.Handler
}

// Session is the signed-in state of a browser.
type Session struct {
	ExpiresAt time.Time `json:"expiresAt"`
}

// SessionDuration is how long a sign in lasts.
const SessionDuration = 7 * 24 * time.Hour

// BuiltVite returns Vite serving the embedded build. Local development uses
// one that loads assets from the Vite dev server instead.
func BuiltVite() (*vite.Vite, error) {
	output, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	return vite.New(vite.Config{Output: output})
}

// NewApp returns the App on s, which is nil when REDIS_URL is not set, with
// assets from v and the rest configured from the environment.
func NewApp(s *store.Store, v *vite.Vite) (*App, error) {
	app := &App{Store: s, Vite: v, Password: os.Getenv("SETUP_PASSWORD")}
	var err error
	if app.Templates, err = templates.Parse(v); err != nil {
		return nil, err
	}
	if app.Session, app.Flash, err = cookies(app.Password, os.Getenv("VERCEL") != ""); err != nil {
		return nil, err
	}
	return app, nil
}

// Handler returns every route. Form posts from other origins are rejected,
// and the setup routes redirect to /login until the browser signs in.
// Assets from the Vite build are served before the routes.
func (a *App) Handler() http.Handler {
	mux := httpx.Fallback(http.NewServeMux(), a.handleError)
	mux.Route("POST "+TelegramWebhookPath, a.Telegram)
	mux.Route("GET /favicon.png", a.Favicon)

	site := httpx.Fallback(httpx.Use(mux, http.NewCrossOriginProtection().Handler), a.handlePageError)
	site.Route("GET /login", a.LoginPage)
	site.Route("POST /login", a.Login)
	site.Route("POST /logout", a.Logout)

	setup := httpx.Use(site, a.RequireSessionMiddleware)
	setup.Route("GET /{$}", a.Index)
	setup.Route("POST /setup/token", a.SetupToken)
	setup.Route("POST /setup/reset", a.SetupReset)
	setup.Route("POST /setup/connect", a.SetupConnect)
	setup.Route("POST /setup/disconnect", a.SetupDisconnect)
	setup.Route("POST /setup/users/allow", a.SetupUsersAllow)
	setup.Route("POST /setup/users/remove", a.SetupUsersRemove)
	setup.Route("POST /setup/users/dismiss", a.SetupUsersDismiss)

	if a.Workflow != nil {
		mux.Handle("/v1/workflow/", a.Workflow)
	}
	return a.Vite.ServePublic(workflow.Middleware(mux, a.Client))
}

// handleError answers with an HTTP error's own status and message, and
// logs any other error and answers 500, so Telegram redelivers a failed
// update.
func (a *App) handleError(w http.ResponseWriter, r *http.Request, err error) {
	if httpErr, ok := httperrors.Unwrap(err); ok {
		message, status, _ := httperrors.Parse(httpErr)
		http.Error(w, message, status)
		return
	}
	log.Printf("%s %s: %v", r.Method, r.URL.Path, cmp.Or(errors.Unwrap(err), err))
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handlePageError answers the pages' routes like handleError, but with an
// error page. Other errors are logged and shown only as a generic message.
func (a *App) handlePageError(w http.ResponseWriter, r *http.Request, err error) {
	page := pages.Error{Status: http.StatusInternalServerError, Message: "Something went wrong. Try again in a moment."}
	if httpErr, ok := httperrors.Unwrap(err); ok {
		page.Message, page.Status, _ = httperrors.Parse(httpErr)
	} else {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, cmp.Or(errors.Unwrap(err), err))
	}
	if renderErr := a.render(w, page.Status, page); renderErr != nil {
		log.Printf("%s %s: render error page: %v", r.Method, r.URL.Path, renderErr)
	}
}
