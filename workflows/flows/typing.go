package flows

import (
	"context"
	"time"

	"github.com/digitallysavvy/go-ai/pkg/provider"
	"github.com/digitallysavvy/go-ai/pkg/provider/types"
)

// typingEvery is how often the chat is told the bot is typing while the
// model answers. Telegram shows the action for about five seconds.
const typingEvery = 4 * time.Second

// typingModel shows the user that the bot is typing for as long as each of
// its model calls runs, so a slow answer doesn't look like no answer. The
// model is only called in steps, so the calls to Telegram never replay.
type typingModel struct {
	provider.LanguageModel
	flows  *Flows
	chatID int64
}

func (m typingModel) DoGenerate(ctx context.Context, opts *provider.GenerateOptions) (*types.GenerateResult, error) {
	stop := m.keepTyping(ctx)
	defer stop()
	return m.LanguageModel.DoGenerate(ctx, opts)
}

// keepTyping sends the typing action now and then every typingEvery until
// stop is called. Failures are ignored: it only shows progress.
func (m typingModel) keepTyping(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		bot, err := m.flows.bot(ctx)
		if err != nil {
			return
		}
		ticker := time.NewTicker(typingEvery)
		defer ticker.Stop()
		for {
			_ = bot.SendChatAction(ctx, m.chatID, "typing")
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
