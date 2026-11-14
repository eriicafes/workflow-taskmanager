package handler

import (
	"log"
	"net/http"

	vercelworld "github.com/eriicafes/go-workflow/worlds/vercel"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/workflows"
	"github.com/eriicafes/workflow-taskmanager/workflows/flows"
)

// queue is built once per function instance, which Vercel keeps warm. It
// can only fail on configuration, which changing takes a redeploy, so a
// failure is kept and every delivery reports it. Vercel builds every file in
// api as its own function, so each builds its own.
var queue, queueErr = newQueue()

// newQueue consumes deliveries of the Vercel World with Task Manager's
// runners.
func newQueue() (http.Handler, error) {
	world, err := workflows.NewWorld()
	if err != nil {
		return nil, err
	}
	s, err := store.Open()
	if err != nil {
		return nil, err
	}
	f, err := flows.New(s)
	if err != nil {
		return nil, err
	}
	return vercelworld.Queue(world, f.Runners()), nil
}

// Queue consumes the workflow topic and runs workflows and their steps.
// vercel.json gives it a queue trigger, so it has no public URL.
func Queue(w http.ResponseWriter, r *http.Request) {
	if queueErr != nil {
		log.Printf("queue: %v", queueErr)
		http.Error(w, "server is not configured", http.StatusInternalServerError)
		return
	}
	workflows.UseOIDCToken(r)
	queue.ServeHTTP(w, r)
}
