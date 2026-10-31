package llm

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestBundledRoleExclusionsAllowGeneralModels(t *testing.T) {
	doc, err := LoadRoleExclusions()
	testutil.FailErr(t, "load exclusions", err)
	for _, kind := range []string{"openai", "together", "gemini", "ollama", "azure"} {
		for _, id := range []string{"gpt-5-nano", "Qwen/Qwen3.5-9B", "Qwen/Qwen3.8-Flash", "gemini-2.0-flash", "model-1b", "tiny"} {
			model := modelinfo.Entry{ID: id, ContextLength: 8192, Capabilities: modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture")}}
			if got := ModelRoleEligibility(kind, model, PolicySlotCoordinator, doc); got.State != "eligible" {
				t.Fatalf("%s/%s: %+v", kind, id, got)
			}
		}
	}
}

func TestRoleEligibilityRequiresPositiveEvidence(t *testing.T) {
	for _, tc := range []struct {
		tools       modelinfo.CapabilityState
		role, state string
	}{
		{modelinfo.CapabilitySupported, PolicySlotCoordinator, "eligible"},
		{modelinfo.CapabilityUnknown, PolicySlotCoordinator, "unverified"},
		{modelinfo.CapabilityUnsupported, PolicySlotCoordinator, "incompatible"},
		{modelinfo.CapabilityUnknown, PolicySlotAgentPool, "unverified"},
		{modelinfo.CapabilityUnknown, PolicySlotLite, "eligible"},
	} {
		model := modelinfo.Entry{ID: "model", Capabilities: modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(tc.tools, "fixture")}}
		got := ModelRoleEligibility("ollama", model, tc.role, RoleExclusions{})
		if got.State != tc.state {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}

func TestRoleExclusionIsProviderAndRoleScoped(t *testing.T) {
	doc := RoleExclusions{Rules: []RoleExclusion{{ID: "closed-tool-interface", Providers: []string{"together"}, Models: []string{"special-*"}, Roles: []string{PolicySlotCoordinator}, Reason: "No tool interface", Evidence: "Fixture"}}}
	model := modelinfo.Entry{ID: "special-chat", Capabilities: modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture")}}
	if got := ModelRoleEligibility("together", model, PolicySlotCoordinator, doc); got.State != "incompatible" || got.RuleID != "closed-tool-interface" {
		t.Fatalf("missing rule: %+v", got)
	}
	if got := ModelRoleEligibility("together", model, PolicySlotLite, doc); got.State != "eligible" {
		t.Fatalf("utility overblocked: %+v", got)
	}
	if got := ModelRoleEligibility("ollama", model, PolicySlotCoordinator, doc); got.State != "eligible" {
		t.Fatalf("other host overblocked: %+v", got)
	}
}

func TestLoadRoleExclusionsRejectsInvalidRules(t *testing.T) {
	for _, body := range []string{"coordinator: [", "rules: [{id: incomplete}]", "rules: []\nunknown: true"} {
		t.Run(body, func(t *testing.T) {
			configtest.Overlay(t, map[config.Rel]string{config.ModelRoleExclusions: body})
			if _, err := LoadRoleExclusions(); err == nil {
				t.Fatal("invalid exclusions accepted")
			}
		})
	}
}

func TestModelIDPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, id string
		want        bool
	}{
		{"Qwen/*-9B", "qwen/qwen3.5-9b", true}, {"special-*", "ordinary", false}, {"re:qwen-[0-9]+", "qwen-9", true}, {"re:qwen-[0-9]+", "prefix-qwen-9", false},
	} {
		got, err := MatchModelIDPattern(tc.pattern, tc.id)
		testutil.FailErr(t, "match pattern", err)
		if got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
	if _, err := MatchModelIDPattern("**", "x"); err == nil {
		t.Fatal("invalid glob accepted")
	}
}

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
	wireCapture := &modelcall.WireCapture{}
	_, err := provider.Complete(t.Context(), modelcall.CompletionRequest{Model: model, ControlCapture: &modelcall.RequestControlCapture{Wire: wireCapture},
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "private task"}},
		Tools:    []tools.ToolMeta{{Name: "read"}}, Debug: modelcall.RequestDebug{SessionID: "session"}})
	testutil.FailErr(t, "complete fixture request", err)
	raw, source, dispatches := wireCapture.Snapshot()
	var exact map[string]any
	testutil.FailErr(t, "decode exact wire", json.Unmarshal(raw, &exact))
	if dispatches != 1 || !reflect.DeepEqual(sent, exact) {
		t.Fatal("debug wrapper replaced the collection capture")
	}
	if source == nil || len(source.Messages) != 1 || source.Messages[0].Content != "private task" {
		t.Fatalf("adapter dispatch source = %+v", source)
	}
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

func TestRouterRebindingPreservesRoundRobinPerScope(t *testing.T) {
	router := testRouter(t)
	first, second := t.TempDir(), t.TempDir()
	for round := range 6 {
		want := fakeModelPolicy.AgentPool.Models[round%2]
		for _, root := range []string{"", first, second} {
			bound := router.WithScope(SettingsScopeGlobal, "")
			if root != "" {
				if round%2 == 0 {
					bound = router.WithScope(SettingsScopeProject, root)
				} else {
					bound = router.WithOverlayRoots([]string{root})
				}
			}
			got, err := bound.Select(t.Context())
			testutil.FailErr(t, "select scoped worker", err)
			if got.ProviderID != want.ProviderID || got.Model != want.Model {
				t.Fatalf("round %d scope %q: got %+v, want %+v", round, root, got, want)
			}
		}
	}
}

func TestConcurrentRouterBindingsDistributePoolEvenly(t *testing.T) {
	router := testRouter(t)
	root := t.TempDir()
	const count = 40
	selections := make(chan string, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			got, err := router.WithOverlayRoots([]string{root}).Select(t.Context())
			if err != nil {
				t.Errorf("select concurrent worker: %v", err)
				return
			}
			selections <- got.Model
		})
	}
	workers.Wait()
	close(selections)
	counts := make(map[string]int)
	for model := range selections {
		counts[model]++
	}
	for _, ref := range fakeModelPolicy.AgentPool.Models {
		if counts[ref.Model] != count/2 {
			t.Errorf("model %s selected %d times, want %d", ref.Model, counts[ref.Model], count/2)
		}
	}
}

func TestRouterEquivalentRootsShareCursor(t *testing.T) {
	router := testRouter(t)
	root := t.TempDir()
	for i, roots := range [][]string{{root}, {root + "/."}, {root, root + "/."}} {
		got, err := router.WithOverlayRoots(roots).Select(t.Context())
		testutil.FailErr(t, "select equivalent roots", err)
		if got.Model != fakeModelPolicy.AgentPool.Models[i%2].Model {
			t.Fatalf("roots %v restarted the cursor: %+v", roots, got)
		}
	}
}

func TestRouterPolicyRefreshAndCanceledSelection(t *testing.T) {
	router := testRouter(t)
	router.policy.globalPath = filepath.Join(t.TempDir(), "model-policy.yaml")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := router.WithOverlayRoots(nil).Select(ctx); err == nil {
		t.Fatal("canceled selection succeeded")
	}
	got, err := router.WithOverlayRoots(nil).Select(t.Context())
	testutil.FailErr(t, "select after cancellation", err)
	if got.Model != fakeModelPolicy.AgentPool.Models[0].Model {
		t.Fatalf("cancellation advanced cursor: %+v", got)
	}
	policy := fakeModelPolicy
	policy.AgentPool.Models = []ModelRef{{ProviderID: "new-provider", Model: "new-model"}}
	testutil.FailErr(t, "replace global pool", router.policy.PutGlobal(policy))
	got, err = router.WithScope(SettingsScopeGlobal, "").Select(t.Context())
	testutil.FailErr(t, "select refreshed policy", err)
	if got.Model != "new-model" || got.ProviderID != "new-provider" {
		t.Fatalf("stale pool after policy replacement: %+v", got)
	}
}
