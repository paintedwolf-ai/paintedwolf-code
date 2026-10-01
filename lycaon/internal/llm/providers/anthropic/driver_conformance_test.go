package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAnthropicDriverConformance(t *testing.T) {
	const apiKey = "conformance-key"
	var gotKey string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		gotKey = r.Header.Get("x-api-key")
		resp := Response{
			Content: []ContentBlock{{
				Type: "text",
				Text: "pong",
			}},
			StopReason: "end_turn",
			Usage:      &Usage{InputTokens: new(4), OutputTokens: new(2)},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	provider := New("anthropic", srv.URL, apiKey, []modelinfo.Entry{{ID: "claude-test"}})
	completion, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Model: "claude-test",
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "ping",
		}},
	})
	testutil.FailErr(t, "Complete", err)
	if completion.Content == "" && len(completion.ToolCalls) == 0 {
		t.Fatal("expected non-empty content or tool calls")
	}
	if gotKey != apiKey {
		t.Fatalf("x-api-key = %q, want %q", gotKey, apiKey)
	}
}
