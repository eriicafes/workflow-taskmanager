// Package flows holds Task Manager's workflow runners and the steps they
// call, as methods on Flows, which carries their dependencies.
package flows

import (
	"context"

	"github.com/digitallysavvy/go-ai/pkg/provider"
	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/worlds/core"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

// Flows runs Task Manager's workflows. Its methods are the runners and the
// steps they call, so steps reach the dependencies through the receiver
// rather than their recorded input.
type Flows struct {
	Store *store.Store
	Model provider.LanguageModel
}

// New returns Flows on s, with the model configured from the environment.
func New(s *store.Store) (*Flows, error) {
	model, err := NewModel()
	if err != nil {
		return nil, err
	}
	return &Flows{Store: s, Model: model}, nil
}

// Runners returns the runners of Task Manager's workflows.
func (f *Flows) Runners() core.RunnerSet {
	return core.Runners(
		workflow.Use(f.Conversation),
		workflow.Use(f.Reminder),
		workflow.Use(f.Agenda),
	)
}

// bot returns a client for the connected bot. The token is read from the
// store on every use, so a bot changed on the setup page takes effect at once.
func (f *Flows) bot(ctx context.Context) (*telegram.Client, error) {
	return workflows.Bot(ctx, f.Store)
}
