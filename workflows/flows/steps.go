package flows

import (
	"context"
	"errors"
	"strings"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/workflowai"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
)

// scoped is the input of a step that acts for one user.
type scoped[In any] struct {
	ChatID int64 `json:"chatId"`
	Input  In    `json:"input"`
}

// state is everything the agent knows about a user at the start of a turn.
type state struct {
	User    store.User                `json:"user"`
	Tasks   []store.Task              `json:"tasks"`
	Habits  []store.Habit             `json:"habits"`
	History []workflowai.ModelMessage `json:"history"`
	// Summary covers the messages folded out of History.
	Summary string `json:"summary"`
}

func (f *Flows) loadState(ctx context.Context, chatID int64) (state, error) {
	user, err := f.Store.User(ctx, chatID)
	if errors.Is(err, store.ErrNotFound) {
		return state{}, workflow.FatalError(err)
	} else if err != nil {
		return state{}, err
	}
	tasks, err := f.Store.Tasks(ctx, chatID)
	if err != nil {
		return state{}, err
	}
	habits, err := f.Store.Habits(ctx, chatID)
	if err != nil {
		return state{}, err
	}
	history, err := f.Store.History(ctx, chatID)
	if err != nil {
		return state{}, err
	}
	summary, err := f.Store.Summary(ctx, chatID)
	if err != nil {
		return state{}, err
	}
	return state{User: user, Tasks: tasks, Habits: habits, History: history, Summary: summary.Text}, nil
}

type historyAppend struct {
	ChatID   int64                     `json:"chatId"`
	Messages []workflowai.ModelMessage `json:"messages"`
}

func (f *Flows) appendHistory(ctx context.Context, input historyAppend) (struct{}, error) {
	return struct{}{}, f.Store.AppendHistory(ctx, input.ChatID, input.Messages)
}

// reply is a message to the user.
type reply struct {
	ChatID int64  `json:"chatId"`
	Text   string `json:"text"`
}

func (f *Flows) sendReply(ctx context.Context, r reply) (struct{}, error) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return struct{}{}, nil
	}
	bot, err := f.bot(ctx)
	if err != nil {
		return struct{}{}, err
	}
	_, err = bot.SendMessage(ctx, telegram.SendMessageParams{ChatID: r.ChatID, Text: text})
	return struct{}{}, err
}

func userMessage(text string) workflowai.ModelMessage {
	return workflowai.ModelMessage{Role: workflowai.RoleUser, Content: []workflowai.ContentPart{{Type: workflowai.PartText, Text: text}}}
}
