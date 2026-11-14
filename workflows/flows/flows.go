// Package flows holds Task Manager's workflow runners and the steps they
// call, as methods on Flows, which carries their dependencies.
package flows

import (
	"context"

	"github.com/digitallysavvy/go-ai/pkg/provider"
	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/worlds"
	"github.com/eriicafes/got"

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

// GetFlows returns Flows configured from the environment.
var GetFlows = got.TryUsing(func(c *got.Container) (*Flows, error) {
	s, err := store.GetStore.From(c)
	if err != nil {
		return nil, err
	}
	model, err := GetModel.From(c)
	if err != nil {
		return nil, err
	}
	return &Flows{Store: s, Model: model}, nil
})

// GetRunners returns the runners of Task Manager's workflows, building
// Flows first.
var GetRunners = got.TryUsing(func(c *got.Container) (worlds.RunnerSet, error) {
	f, err := GetFlows.From(c)
	if err != nil {
		return nil, err
	}
	return worlds.Runners(
		workflow.Use(f.Conversation),
		workflow.Use(f.Reminder),
		workflow.Use(f.Agenda),
	), nil
})

// bot returns a client for the connected bot. The token is read from the
// store on every use, so a bot changed on the setup page takes effect at once.
func (f *Flows) bot(ctx context.Context) (*telegram.Client, error) {
	return workflows.Bot(ctx, f.Store)
}
