package workflows

import (
	"net/http"
	"os"

	vercelworld "github.com/eriicafes/go-workflow/worlds/vercel"
)

// DefaultTopic is the Vercel Queues topic vercel.json subscribes the queue
// function to.
const DefaultTopic = "taskmanager-workflow"

// NewWorld returns the Vercel World configured from the environment. It
// keeps runs in Vercel's managed workflow storage and delivers them through
// Vercel Queues in the run's region, both authenticated with the deployment's
// OIDC token. Clients are built from it with workflow.NewClient.
func NewWorld() (*vercelworld.World, error) {
	config := vercelworld.Config{Topic: DefaultTopic}
	if os.Getenv("VERCEL_PROJECT_PRODUCTION_URL") != "" {
		config.BaseURL = "https://" + os.Getenv("VERCEL_PROJECT_PRODUCTION_URL") + "/v1/workflow"
	}
	return vercelworld.New(config)
}

// UseOIDCToken makes the OIDC token of a Vercel function's request visible
// to the World and the AI Gateway, which read it from VERCEL_OIDC_TOKEN.
// Vercel sends it with every request and rotates it.
func UseOIDCToken(r *http.Request) {
	if token := r.Header.Get("x-vercel-oidc-token"); token != "" && token != os.Getenv("VERCEL_OIDC_TOKEN") {
		os.Setenv("VERCEL_OIDC_TOKEN", token)
	}
}
