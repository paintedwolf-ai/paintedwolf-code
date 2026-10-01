package openaicompat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFixedNativeEffortWinsEveryRequestControl(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	model := "@cf/zai-org/glm-5.3-flash"
	o := modelcall.ThinkingOverride{ProviderID: "p", Model: model, Mode: "fixed", Effort: "high"}
	for _, profile := range []providerprofile.Profile{providerprofile.CloudflareWorkersAI(), providerprofile.Together(), providerprofile.Openrouter()} {
		p := New("p", "https://example.invalid", "", nil).WithProfile(profile)
		for _, req := range []modelcall.CompletionRequest{
			{Think: modelcall.ThinkHigh},
			{Think: modelcall.ThinkOff, Composition: modelcall.CompositionHostUtility},
			{Debug: modelcall.RequestDebug{ParentSessionID: "worker"}},
		} {
			req.Model, req.ThinkingOverride = model, &o
			for _, opts := range []controlOpts{{}, {strictRetry: true}, {reasoning: reasoningFallbackOmit}, {reasoning: reasoningFallbackOff}} {
				controls := p.resolveRequestControls(req, model, opts)
				effort := controls.ReasoningEffort
				if controls.Reasoning != nil {
					effort = controls.Reasoning.Effort
				}
				if effort != "high" {
					t.Fatalf("native high was rewritten: %+v", controls)
				}
			}
		}
	}
	local := ollama.New("p", "http://localhost:11434", "", []modelinfo.Entry{{ID: model, ContextLength: 8192}})
	prepared, _, err := local.Prepare(t.Context(), modelcall.CompletionRequest{Model: model, ThinkingOverride: &o, Think: modelcall.ThinkOff}, false)
	testutil.FailErr(t, "prepare Ollama thinking override", err)
	if got := prepared.Think; got != "high" {
		t.Fatalf("Ollama override lost: %v", got)
	}
}

func TestFixedEffortDoesNotRetryWithWeakerControl(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode wire: %v", err)
		}
		if body["reasoning_effort"] != "high" {
			t.Errorf("fixed native effort changed: %v", body["reasoning_effort"])
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: reasoning_effort","type":"invalid_request_error","param":"reasoning_effort","code":"unsupported_parameter"}}`))
	}))
	defer server.Close()
	p := New("p", server.URL, "fixture", []modelinfo.Entry{{ID: "model", ThinkStyle: "effort_levels"}})
	req := modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}}, ThinkingOverride: &modelcall.ThinkingOverride{Mode: "fixed", Effort: "high"}}
	for _, stream := range []bool{false, true} {
		requests.Store(0)
		var err error
		if stream {
			_, err = p.Stream(t.Context(), req)
		} else {
			_, err = p.Complete(t.Context(), req)
		}
		if err == nil || requests.Load() != 1 {
			t.Fatalf("stream=%v: override silently retried (%d calls): %v", stream, requests.Load(), err)
		}
	}
}
