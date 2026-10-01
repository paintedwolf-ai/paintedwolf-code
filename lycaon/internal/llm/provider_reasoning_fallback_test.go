package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// Explicit off is distinct from omitting the reasoning field.
func TestReasoningFallbackReachesExplicitOffOnTheWire(t *testing.T) {
	t.Cleanup(openaicompat.ResetReasoningProbeForTest)

	var efforts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire map[string]any
		testutil.FailErr(t, "decode request", json.NewDecoder(r.Body).Decode(&wire))
		effort, _ := wire["reasoning_effort"].(string)
		efforts = append(efforts, effort)
		if effort != providerprofile.OpenAIReasoningEffortOff {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"message": "Function tools with reasoning_effort are not supported for m in /v1/chat/completions. " +
					"To use function tools, use /v1/responses or set reasoning_effort to 'none'.",
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
		})
	}))
	defer srv.Close()

	built, err := driverFactories.Build(context.Background(), "openai", providerBuild{
		entry: CatalogEntry{
			ID:      "openai-1",
			Kind:    "openai",
			BaseURL: srv.URL,
			Models:  []modelinfo.Entry{{ID: "m", MaxTokens: 4096}},
		},
		apiKey: "k",
	})
	testutil.FailErr(t, "build provider", err)

	req := modelcall.CompletionRequest{
		Model: "m",
		Think: modelcall.ThinkHigh,
		Tools: []tools.ToolMeta{{Name: "task"}},
	}
	out, err := built.(*openaicompat.Provider).Complete(context.Background(), req)
	testutil.FailErr(t, "complete", err)
	if out.Content != "ok" {
		t.Fatalf("content = %q, want the answer from the retry", out.Content)
	}
	if len(efforts) != 3 || efforts[0] != "high" || efforts[1] != "" || efforts[2] != providerprofile.OpenAIReasoningEffortOff {
		t.Fatalf("reasoning_effort per attempt = %q, want [high omitted none]", efforts)
	}

	// Reuse the accepted reasoning setting.
	efforts = nil
	_, err = built.(*openaicompat.Provider).Complete(context.Background(), req)
	testutil.FailErr(t, "second complete", err)
	if len(efforts) != 1 || efforts[0] != providerprofile.OpenAIReasoningEffortOff {
		t.Fatalf("second turn efforts = %q, want [none] from the probe cache", efforts)
	}
}
