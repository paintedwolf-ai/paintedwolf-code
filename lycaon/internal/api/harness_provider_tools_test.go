package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHarnessToolsUsesRealCallsWithoutChangingEligibility(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	var calls atomic.Int32
	var refresh func() error
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"model-x"},{"id":"model-y"}]}`)
			return
		}
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode provider request: %v", err)
			return
		}
		name := "read_status"
		args := map[string]any{}
		if calls.Add(1) == 2 {
			name = "record_status"
			for _, message := range req.Messages {
				if message.Role == "tool" {
					var result map[string]string
					if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.SplitN(message.Content, "\n", 2)[1], "⟦D⟧")), &result); err != nil {
						t.Errorf("decode tool receipt: %v", err)
						return
					}
					args["receipt"] = result["receipt"]
				}
			}
		}
		if calls.Load() == 1 && refresh != nil {
			if err := refresh(); err != nil {
				t.Errorf("refresh registry during diagnostic: %v", err)
				return
			}
		}
		argJSON, _ := json.Marshal(args)
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-result", "type": "function", "function": map[string]any{"name": name, "arguments": string(argJSON)}}}}, "finish_reason": "tool_calls"}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
	}))
	defer upstream.Close()
	catalog := strings.Replace(fakeProvidersYAML(upstream.URL), "tools: {state: supported}", "tools: {state: unknown}", 1)
	server, base := newProviderTestServerWithCatalogs(t, catalog, catalog)
	refresh = func() error {
		return server.Admin.Project.Verification.LLMService.Registry.RefreshModels(t.Context(), testProviderID)
	}
	ref := llm.ModelRef{ProviderID: testProviderID, Model: "model-x"}
	testutil.FailErr(t, "assign chat model with unlisted tools without paid calls", server.Admin.Project.Verification.LLMService.ValidateModelRef(t.Context(), ref, llm.PolicySlotCoordinator))
	if calls.Load() != 0 {
		t.Fatal("assignment made paid calls")
	}
	req := httptest.NewRequest(http.MethodPost, base+"/harness/llm/provider-tools", strings.NewReader(`{"allow_live":true,"provider":"`+testProviderID+`","model":"model-x"}`))
	req.Header.Set("Authorization", "Bearer "+TestAPIToken)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("diagnostic status %d: %s", response.Code, response.Body.String())
	}
	var result harnessToolResult
	testutil.FailErr(t, "decode diagnostic response", json.Unmarshal(response.Body.Bytes(), &result))
	if !result.OK || calls.Load() != 2 {
		t.Fatalf("diagnostic=%+v, calls=%d", result, calls.Load())
	}
	testutil.FailErr(t, "assign verified model", server.Admin.Project.Verification.LLMService.ValidateModelRef(t.Context(), ref, llm.PolicySlotCoordinator))
	if result.ReasoningPolicy == nil || result.DriverSHA256 == "" || result.RequestPolicySHA256 == "" {
		t.Fatal("diagnostic lost request provenance")
	}
	var meta wire.ProviderMeta
	for _, candidate := range server.Admin.Project.Verification.LLMService.Registry.List(t.Context()) {
		if candidate.ID == testProviderID {
			meta = candidate
			break
		}
	}
	if len(meta.Models) == 0 {
		t.Fatal("provider model metadata missing")
	}
	if meta.Models[0].Eligibility.Coordinator.State != "unverified" {
		t.Fatal("diagnostic changed assignment evidence")
	}
}
