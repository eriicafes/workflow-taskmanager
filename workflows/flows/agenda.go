package flows

import (
	"context"
	"time"

	"github.com/eriicafes/go-workflow"

	"github.com/eriicafes/workflow-taskmanager/lib/agenda"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

// Agenda sends the user the day's tasks and habits at input.At, then starts
// the run for the next agenda. Each user has one chain of these runs instead
// of a cron job, and starting a new run every day keeps each run's event log
// short. The store records the chain's current run: a run that finds another
// recorded, because the user changed their agenda time and a new chain
// started, ends without sending.
func (f *Flows) Agenda(ctx context.Context, input workflows.Agenda) (workflows.AgendaResult, error) {
	var result workflows.AgendaResult
	self := workflow.GetWorkflowMetadata(ctx).RunID
	now := workflow.Now(ctx)
	if wait := input.At.Sub(now); wait > 0 {
		if err := workflow.Sleep(ctx, wait); err != nil {
			return result, err
		}
	}
	sent, err := workflow.Step(ctx, f.sendAgenda, scoped[string]{ChatID: input.ChatID, Input: self})
	if err != nil || !sent.Current {
		return result, err
	}
	result.Sent = sent.Sent
	run, err := workflows.StartOnLatest(ctx, workflows.Agenda{ChatID: input.ChatID, At: sent.Next})
	if err != nil {
		return result, err
	}
	_, err = workflow.Step(ctx, f.continueAgenda, agendaHandoff{ChatID: input.ChatID, Prev: self, Next: run.ID()})
	return result, err
}

type agendaSent struct {
	// Current reports whether the run is still the user's agenda chain.
	Current bool      `json:"current"`
	Sent    bool      `json:"sent"`
	Next    time.Time `json:"next"`
}

// sendAgenda sends the day's agenda, unless the user paused or turned it
// off, or has nothing on, and returns when the next one is due. Input is the
// ID of the run sending it.
func (f *Flows) sendAgenda(ctx context.Context, input scoped[string]) (agendaSent, error) {
	current, err := f.Store.AgendaRun(ctx, input.ChatID)
	if err != nil || current != input.Input {
		return agendaSent{}, err
	}
	user, err := f.Store.User(ctx, input.ChatID)
	if err != nil {
		return agendaSent{}, err
	}
	now := time.Now().In(user.Location())
	result := agendaSent{Current: true, Next: agenda.Next(user.AgendaHour, user.Location(), now)}
	// The hour may have changed while the run slept.
	if user.Paused || user.AgendaHour < 0 || now.Hour() != user.AgendaHour {
		return result, nil
	}
	tasks, err := f.Store.Tasks(ctx, input.ChatID)
	if err != nil {
		return agendaSent{}, err
	}
	habits, err := f.Store.Habits(ctx, input.ChatID)
	if err != nil {
		return agendaSent{}, err
	}
	text := agenda.Today(tasks, habits, now)
	if text == "" {
		return result, nil
	}
	bot, err := f.bot(ctx)
	if err != nil {
		return agendaSent{}, err
	}
	if _, err := bot.SendMessage(ctx, telegram.SendMessageParams{ChatID: input.ChatID, Text: text}); err != nil {
		return agendaSent{}, err
	}
	result.Sent = true
	return result, f.Store.Note(ctx, input.ChatID, "Sent the user their agenda for today.")
}

type agendaHandoff struct {
	ChatID int64  `json:"chatId"`
	Prev   string `json:"prev"`
	Next   string `json:"next"`
}

// continueAgenda records the next run as the user's agenda chain, unless a
// new chain replaced this one meanwhile.
func (f *Flows) continueAgenda(ctx context.Context, h agendaHandoff) (bool, error) {
	return f.Store.ContinueAgendaRun(ctx, h.ChatID, h.Prev, h.Next)
}

// saveAgendaRun records the run that now carries the user's agenda.
func (f *Flows) saveAgendaRun(ctx context.Context, input scoped[string]) (struct{}, error) {
	return struct{}{}, f.Store.SetAgendaRun(ctx, input.ChatID, input.Input)
}
