package flows

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/digitallysavvy/go-ai/pkg/provider"
	"github.com/digitallysavvy/go-ai/pkg/provider/types"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/store/storetest"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
)

// slowModel answers once its call is released, counting typing actions sent
// meanwhile.
type slowModel struct {
	provider.LanguageModel
	release chan struct{}
}

func (m slowModel) DoGenerate(ctx context.Context, opts *provider.GenerateOptions) (*types.GenerateResult, error) {
	<-m.release
	return &types.GenerateResult{Text: "done"}, nil
}

func TestTypingModelTypesWhileGenerating(t *testing.T) {
	var typing atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
			typing.Add(1)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	t.Cleanup(server.Close)
	previous := telegram.APIURL
	telegram.APIURL = server.URL
	t.Cleanup(func() { telegram.APIURL = previous })

	f := &Flows{Store: storetest.New(t)}
	if err := f.Store.SaveBot(t.Context(), store.Bot{Token: "1:test"}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	model := typingModel{LanguageModel: slowModel{release: release}, flows: f, chatID: 1}
	go func() {
		for typing.Load() == 0 {
		}
		close(release)
	}()
	result, err := model.DoGenerate(t.Context(), &provider.GenerateOptions{})
	if err != nil || result.Text != "done" {
		t.Fatalf("DoGenerate = %v, %v", result, err)
	}
	sent := typing.Load()
	if sent == 0 {
		t.Fatal("no typing action while the model answered")
	}
	// It stops once the call returns.
	if typing.Load() != sent {
		t.Fatal("typing continued after the call")
	}
}
