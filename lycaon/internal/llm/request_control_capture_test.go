package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRequestControlCaptureMatchesSentWireWithoutPrompt(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", path)
	observability.CloseLLMDebug()
	t.Cleanup(observability.CloseLLMDebug)
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		testutil.FailErr(t, "read provider wire", json.NewDecoder(r.Body).Decode(&sent))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	model := "zai-org/GLM-5.3-Flash"
	provider := wrapProviderIfDebug(openaicompat.New("fixture", server.URL, "fixture", []modelinfo.Entry{{ID: model}}).WithProfile(providerprofile.Together()))
	_, err := provider.Complete(t.Context(), modelcall.CompletionRequest{Model: model,
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "private task"}},
		Tools:    []tools.ToolMeta{{Name: "read"}}, Debug: modelcall.RequestDebug{SessionID: "session"}})
	testutil.FailErr(t, "complete fixture request", err)
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read capture", err)
	var captured struct {
		Controls []map[string]any `json:"request_controls"`
	}
	testutil.FailErr(t, "decode capture", json.Unmarshal(body, &captured))
	if len(captured.Controls) != 1 || len(captured.Controls[0]) != 2 {
		t.Fatalf("unexpected request control projection: %+v", captured.Controls)
	}
	for _, key := range []string{"reasoning_effort", "max_tokens"} {
		if captured.Controls[0][key] != sent[key] {
			t.Fatalf("captured %s differs from sent value", key)
		}
	}
	// The host default is medium; the GLM rule rounds it down to low.
	if sent["reasoning_effort"] != "low" {
		t.Fatalf("application reasoning policy was not sent: %v", sent["reasoning_effort"])
	}
}

func TestRequestControlCaptureWithholdsOtherGenerationFields(t *testing.T) {
	capture := &modelcall.RequestControlCapture{}
	capture.Record([]byte(`{"messages":[{"content":"private"}],"generationConfig":{"maxOutputTokens":2048,"stopSequences":["private"],"responseMimeType":"application/json","thinkingConfig":{"thinkingBudget":1024,"includeThoughts":true}}}`))
	values := capture.Snapshot()
	if len(values) != 1 || len(values[0]) != 1 {
		t.Fatalf("unexpected projection: %+v", values)
	}
	var generation map[string]any
	testutil.FailErr(t, "decode projected generation", json.Unmarshal(values[0]["generationConfig"], &generation))
	if len(generation) != 2 || generation["maxOutputTokens"] != float64(2048) || generation["thinkingConfig"] == nil {
		t.Fatalf("unexpected generation controls: %+v", generation)
	}
}
