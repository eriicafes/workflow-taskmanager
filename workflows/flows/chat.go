package flows

import (
	"context"
	"slices"

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
		_, _ = workflow.Step(ctx, f.sendReply, reply{ChatID: chatID, Text: "Sorry, something went wrong on my side 😕 Try again in a moment?"})
		return err
	}
	added := result.Messages[len(st.History):]
	if _, err := workflow.Step(ctx, f.appendHistory, historyAppend{ChatID: chatID, Messages: added}); err != nil {
		return err
	}
	text := result.Text
	if text == "" && len(result.ToolResults) == 0 {
		text = "👍"
	}
	if _, err := workflow.Step(ctx, f.sendReply, reply{ChatID: chatID, Text: text}); err != nil {
		return err
	}
	return f.compactHistory(ctx, chatID)
}
