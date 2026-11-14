package flows

import (
	"context"
	"fmt"
	"strings"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/workflowai"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
)

const (
	// compactAt is how many stored messages a conversation reaches before
	// its oldest messages are folded into its summary.
	compactAt = 80
	// keepRecent is about how many messages stay after folding. The cut
	// moves forward to a user message, so a turn is never split.
	keepRecent = 40
)

const summarizerInstructions = `You keep Task Manager's memory of an ongoing Telegram chat between Task Manager, a
task and habit assistant bot, and its user. Rewrite the summary so it covers both the previous
summary and the new part of the conversation.

Keep what will help future chats: the user's routines, goals, projects and plans, how they like
reminders, what they finished or struggled with, open questions, and how the user likes Task
Manager to talk. Tasks and habits themselves are stored elsewhere, so mention them only for what
happened with them. Skip small talk.

Write at most 250 words as short plain-text bullet points, oldest first. Reply with only the
summary.`

type compactionPlan struct {
	// Count is how many of the oldest messages to fold, or 0 for none.
	Count    int                       `json:"count"`
	Messages []workflowai.ModelMessage `json:"messages"`
	Summary  string                    `json:"summary"`
}

type compaction struct {
	ChatID  int64                   `json:"chatId"`
	Count   int                     `json:"count"`
	First   workflowai.ModelMessage `json:"first"`
	Summary store.Summary           `json:"summary"`
}

// compactHistory folds the oldest messages of a conversation into its
// summary once the conversation passes compactAt messages, so the agent
// keeps what they said after they leave its history. It runs after the
// reply is sent, so the user doesn't wait for it.
func (f *Flows) compactHistory(ctx context.Context, chatID int64) error {
	plan, err := workflow.Step(ctx, f.planCompaction, chatID)
	if err != nil || plan.Count == 0 {
		return err
	}
	summarizer := workflowai.NewWorkflowAgent(workflowai.WorkflowAgentOptions{
		ID:           "taskmanager-summarizer",
		Model:        f.Model,
		Instructions: summarizerInstructions,
	})
	result, err := summarizer.Generate(ctx, workflowai.CallOptions{Prompt: summaryPrompt(plan.Summary, plan.Messages)})
	if err != nil {
		return err
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return nil
	}
	now, err := workflow.Now(ctx)
	if err != nil {
		return err
	}
	_, err = workflow.Step(ctx, f.saveCompaction, compaction{
		ChatID:  chatID,
		Count:   plan.Count,
		First:   plan.Messages[0],
		Summary: store.Summary{Text: text, UpdatedAt: now},
	})
	return err
}

// planCompaction picks the oldest messages to fold, if the conversation is
// long enough, with the summary they will be merged into.
func (f *Flows) planCompaction(ctx context.Context, chatID int64) (compactionPlan, error) {
	history, err := f.Store.FullHistory(ctx, chatID)
	if err != nil || len(history) <= compactAt {
		return compactionPlan{}, err
	}
	count := compactionCut(history, keepRecent)
	if count == 0 {
		return compactionPlan{}, nil
	}
	summary, err := f.Store.Summary(ctx, chatID)
	if err != nil {
		return compactionPlan{}, err
	}
	return compactionPlan{Count: count, Messages: history[:count], Summary: summary.Text}, nil
}

// saveCompaction saves the new summary and removes the messages it covers.
// When another run compacted first, it leaves the history alone.
func (f *Flows) saveCompaction(ctx context.Context, c compaction) (bool, error) {
	return f.Store.CompactHistory(ctx, c.ChatID, c.Count, c.First, c.Summary)
}

// compactionCut returns how many of the oldest messages to fold so that
// about keep remain: the index of the first user message among the last
// keep, or 0 when there is none.
func compactionCut(history []workflowai.ModelMessage, keep int) int {
	for i := max(len(history)-keep, 1); i < len(history); i++ {
		if history[i].Role == workflowai.RoleUser {
			return i
		}
	}
	return 0
}

// summaryPrompt asks for summary rewritten to cover messages too.
func summaryPrompt(summary string, messages []workflowai.ModelMessage) string {
	var b strings.Builder
	b.WriteString("Previous summary:\n")
	if summary == "" {
		b.WriteString("(none yet)\n")
	} else {
		b.WriteString(summary + "\n")
	}
	b.WriteString("\nNew part of the conversation:\n")
	b.WriteString(transcript(messages))
	return b.String()
}

// transcript writes messages as plain lines for the summarizer.
func transcript(messages []workflowai.ModelMessage) string {
	var b strings.Builder
	for _, message := range messages {
		for _, part := range message.Content {
			switch {
			case part.Type == workflowai.PartText && message.Role == workflowai.RoleUser:
				if note, ok := strings.CutPrefix(part.Text, store.NotePrefix); ok {
					fmt.Fprintf(&b, "Note: %s\n", note)
				} else {
					fmt.Fprintf(&b, "User: %s\n", part.Text)
				}
			case part.Type == workflowai.PartText && message.Role == workflowai.RoleAssistant:
				fmt.Fprintf(&b, "Task Manager: %s\n", part.Text)
			case part.Type == workflowai.PartToolCall:
				fmt.Fprintf(&b, "Task Manager used %s %s\n", part.ToolName, part.Input)
			}
		}
	}
	return b.String()
}
