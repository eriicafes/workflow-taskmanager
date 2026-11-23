package handler

import (
	"log"
	"net/http"
	"os"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/worlds"
	vercelworld "github.com/eriicafes/go-workflow/worlds/vercel"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/web"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

// index is built once per function instance, which Vercel keeps warm. It
// can only fail on configuration, which changing takes a redeploy, so a
// failure is kept and every request reports it. Vercel builds every file in
// api as its own function, so each builds its own.
var index, indexErr = newIndex()

// newIndex returns the App on the Vercel World, with the World's endpoints.
// Its RPC endpoint needs WORKFLOW_HTTP_TOKEN, and is closed when that is
// unset. Without REDIS_URL the App has no store, so the setup page can still
// say it is missing.
func newIndex() (http.Handler, error) {
	world, err := workflows.NewWorld()
	if err != nil {
		return nil, err
	}
	s, err := store.Open()
	if err != nil && os.Getenv("REDIS_URL") != "" {
		return nil, err
	}
	v, err := web.BuiltVite()
	if err != nil {
		return nil, err
	}
	app, err := web.NewApp(s, v)
	if err != nil {
		return nil, err
	}
	app.Client = workflow.NewClient(world)
	app.Workflow = vercelworld.HTTP(world, worlds.WithClientToken(os.Getenv("WORKFLOW_HTTP_TOKEN")))
	return app.Handler(), nil
}

// Index serves every public route: the setup page and its assets,
// Telegram's webhook, and the World's endpoints. vercel.json rewrites all
// paths to it.
func Index(w http.ResponseWriter, r *http.Request) {
	if indexErr != nil {
		log.Printf("index: %v", indexErr)
		http.Error(w, "server is not configured", http.StatusInternalServerError)
		return
	}
	workflows.UseOIDCToken(r)
	index.ServeHTTP(w, r)
}
