package flows

import (
	"context"
	"errors"
	"time"

	"github.com/eriicafes/go-workflow"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

const (
	// idleTimeout ends a conversation run once its mailbox stays empty
	// this long. The next mail starts a new run.
	idleTimeout = 10 * time.Minute
	// maxTurns ends a conversation run after this many turns, so each run's
	// event log, which every delivery replays, stays short.
	maxTurns = 10
)

// Conversation answers the user's mailbox in order, one turn at a time, so a
// message that arrives while the agent replies or summarizes waits for that
// turn and is answered with its context. Messages sent in a row are answered
// together as one turn.
//
// Mail waits in the store and the hook only wakes the run, so mail that
// arrives as the run ends is not lost: after it stops listening, the run
// checks the mailbox once more and hands what it finds to a new run.
func (f *Flows) Conversation(ctx context.Context, input workflows.Conversation) (workflows.ConversationResult, error) {
	var result workflows.ConversationResult
	hook := workflow.CreateHook[struct{}](ctx, workflow.HookOption{Token: workflows.MailboxToken(input.ChatID)})
	defer hook.Dispose(ctx)
	if owner, err := hook.GetConflict(ctx); err != nil || owner != nil {
		// Another run is listening; it answers the mailbox.
		return result, err
	}

	for result.Turns < maxTurns {
		mails, err := workflow.Step(ctx, f.peekMail, input.ChatID)
		if err != nil {
			return result, err
		}
		if len(mails) == 0 {
			idle, err := waitForMail(ctx, hook)
			if err != nil {
				return result, err
			}
			if idle {
				break
			}
			continue
		}
		turns, failed := f.answerMail(ctx, input.ChatID, mails)
		result.Turns += turns
		result.Failed += failed
		if _, err := workflow.Step(ctx, f.ackMail, scoped[int]{ChatID: input.ChatID, Input: len(mails)}); err != nil {
			return result, err
		}
	}

	// Stop listening, then hand any mail that arrived meanwhile to a new
	// run. Mail delivered after this finds no hook and starts one itself.
	hook.Dispose(ctx)
	pending, err := workflow.Step(ctx, f.mailCount, input.ChatID)
	if err != nil || pending == 0 {
		return result, err
	}
	_, err = workflows.Start(ctx, workflows.Conversation{ChatID: input.ChatID})
	return result, err
}

// waitForMail waits until the hook wakes the run, or reports idle once
// idleTimeout passes first.
func waitForMail(ctx context.Context, hook *workflow.Hook[struct{}]) (idle bool, err error) {
	timerCtx, cancel, err := workflow.WithCancel(ctx)
	if err != nil {
		return false, err
	}
	timer := workflow.Timer(timerCtx, idleTimeout)
	winner, err := workflow.Select(ctx, hook, timer)
	// Cancel the timer when mail came first, so it doesn't wake the run.
	if err := errors.Join(err, cancel()); err != nil {
		return false, err
	}
	if winner == timer {
		return true, nil
	}
	_, err = hook.Next(ctx)
	return false, err
}

// answerMail answers mails as one turn: messages sent in a row are
// answered together. A failed turn has already told the user.
func (f *Flows) answerMail(ctx context.Context, chatID int64, mails []store.Mail) (turns, failed int) {
	texts := make([]string, len(mails))
	for i, mail := range mails {
		texts[i] = mail.Text
	}
	if err := f.chatTurn(ctx, chatID, texts); err != nil {
		return 1, 1
	}
	return 1, 0
}

func (f *Flows) peekMail(ctx context.Context, chatID int64) ([]store.Mail, error) {
	return f.Store.PeekMail(ctx, chatID)
}

func (f *Flows) ackMail(ctx context.Context, input scoped[int]) (struct{}, error) {
	return struct{}{}, f.Store.AckMail(ctx, input.ChatID, input.Input)
}

func (f *Flows) mailCount(ctx context.Context, chatID int64) (int64, error) {
	return f.Store.MailCount(ctx, chatID)
}
