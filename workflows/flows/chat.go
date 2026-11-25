package flows

import (
	"context"
	"slices"
	"strings"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/workflowai"
)

// chatTurn answers messages the user sent in a row as one turn: it runs the
// agent on the history followed by them, saves the new messages, sends the
// reply, and then folds old history into the summary.
func (f *Flows) chatTurn(ctx context.Context, chatID int64, texts []string) error {
	st, err := workflow.Step(ctx, f.loadState, chatID)
	if err != nil {
		return err
	}
	turn := make([]workflowai.ModelMessage, len(texts))
	for i, text := range texts {
		turn[i] = userMessage(text)
	}
	now := workflow.Now(ctx)
	result, err := f.newAgent(st, now).Generate(ctx, workflowai.CallOptions{
		Messages: append(slices.Clone(st.History), turn...),
	})
	if err != nil {
		_, _ = workflow.Step(ctx, f.sendReply, reply{ChatID: chatID, Text: "Ugh, something went wrong on my end 😕 Can you try that again in a bit?"})
		return err
	}
	added := undashMessages(result.Messages[len(st.History):])
	if _, err := workflow.Step(ctx, f.appendHistory, historyAppend{ChatID: chatID, Messages: added}); err != nil {
		return err
	}
	text := undash(result.Text)
	if text == "" && len(result.ToolResults) == 0 {
		text = "👍"
	}
	if _, err := workflow.Step(ctx, f.sendReply, reply{ChatID: chatID, Text: text}); err != nil {
		return err
	}
	return f.compactHistory(ctx, chatID)
}

// dashes replaces the em and en dashes that models write despite being asked
// not to, which read as machine-written in a chat: an em dash becomes a
// comma, and an en dash in a range like 9–5 a hyphen.
var dashes = strings.NewReplacer(" — ", ", ", " —", ",", "—", ", ", " – ", ", ", "–", "-")

// undash returns text without em or en dashes.
func undash(text string) string { return dashes.Replace(text) }

// undashMessages removes em and en dashes from the text the agent wrote in
// messages, so its history never shows it writing them.
func undashMessages(messages []workflowai.ModelMessage) []workflowai.ModelMessage {
	out := slices.Clone(messages)
	for i, message := range out {
		if message.Role != workflowai.RoleAssistant {
			continue
		}
		content := slices.Clone(message.Content)
		for j, part := range content {
			if part.Type == workflowai.PartText {
				content[j].Text = undash(part.Text)
			}
		}
		out[i].Content = content
	}
	return out
}
