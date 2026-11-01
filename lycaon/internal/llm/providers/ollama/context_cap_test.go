package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestOllamaEstimatesToolContentOnce(t *testing.T) {
	content := strings.Repeat("result ", 100)
	for _, message := range []api.Message{
		{Role: api.MessageRoleTool, Content: content},
		{Role: api.MessageRoleTool, Content: content, ToolResult: &api.ToolResult{Content: content}},
		{Role: api.MessageRoleTool, Content: content, ToolResult: &api.ToolResult{Content: "stored original"}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Content: content}},
	} {
		wire := ProjectMessages([]api.Message{message}, MessageProjection{})
		want := estimatePromptTokens([]ollamaChatMessage{{Role: "tool", Content: wire[0].Content}}, nil)
		if got := estimatePromptTokens(wire, nil); got != want {
			t.Fatalf("tool estimate = %d, want wire content estimate %d", got, want)
		}
	}
}

func TestOllamaPromptCalibrationReplacesColdStartPadding(t *testing.T) {
	p := New("remote", "http://unused.invalid", "", nil)
	if got := p.projectPromptTokens("model", 100); got != 150 {
		t.Fatalf("cold projection = %d, want 150", got)
	}
	p.observePromptTokens("model", 100, 100)
	if got := p.projectPromptTokens("model", 100); got != 110 {
		t.Fatalf("calibrated projection = %d, want 110", got)
	}
	p.observePromptTokens("model", 100, 50)
	if got := p.projectPromptTokens("model", 100); got != 110 {
		t.Fatalf("terse prompt reduced learned headroom to %d", got)
	}
	p.observePromptTokens("model", 100, 200)
	if got := p.projectPromptTokens("model", 100); got != 220 {
		t.Fatalf("expanded tokenizer projection = %d, want 220", got)
	}
	p.observePromptTokens("terse", 100, 10)
	if got := p.projectPromptTokens("terse", 100); got != 100 {
		t.Fatalf("projection = %d, must not undercut the raw estimate", got)
	}
	if got := p.projectPromptTokens("unseen", 100); got != 150 {
		t.Fatalf("unseen model projection = %d, want cold-start padding", got)
	}
}

func TestOllamaCanceledContextDiscoveryCannotPoisonLaterRequests(t *testing.T) {
	for _, duringProbe := range []bool{false, true} {
		name := "before_probe"
		if duringProbe {
			name = "during_probe"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if duringProbe && calls.Add(1) == 1 {
					cancel()
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, `{"model_info":{"fixture.context_length":32768}}`)
			}))
			defer server.Close()
			p := New("fixture", server.URL, "", nil)
			if !duringProbe {
				cancel()
			}
			if got := p.ContextLimit(ctx, "model"); got != 0 {
				t.Fatalf("canceled discovery returned %d", got)
			}
			if _, cached := p.capCache["model"]; cached {
				t.Fatal("canceled discovery cached an unknown context window")
			}
			for _, model := range []string{"model", "other", "model"} {
				if got := p.ContextLimit(t.Context(), model); got != 32768 {
					t.Fatalf("later discovery for %s = %d, want 32768", model, got)
				}
			}
		})
	}
}
