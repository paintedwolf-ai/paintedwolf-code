package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	vertexexpressprovider "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
)

func TestReasoningBudgetCannotExceedExplicitOutputLimit(t *testing.T) {
	for _, limit := range []int{512, 1024, 2048, 8192} {
		req := modelcall.CompletionRequest{Model: "fixture", MaxTokens: limit, Think: modelcall.ThinkHigh}
		anthropic := anthropicprovider.New("fixture", "https://example.invalid", "", []modelinfo.Entry{{ID: "fixture", ThinkStyle: "budget_tokens"}}).Prepare(req, false)
		if anthropic.MaxTokens != limit || (anthropic.Thinking != nil && anthropic.Thinking.BudgetTokens >= limit) {
			t.Fatalf("Anthropic exceeded %d: %+v", limit, anthropic)
		}
		vertex := vertexexpressprovider.New("vertex-express", "https://aiplatform.googleapis.com/v1", "k", []modelinfo.Entry{{ID: "fixture"}}).Prepare(req).GenerationConfig
		if vertex.MaxOutputTokens != limit || (vertex.ThinkingConfig != nil && vertex.ThinkingConfig.ThinkingBudget != nil && *vertex.ThinkingConfig.ThinkingBudget >= limit) {
			t.Fatalf("Vertex exceeded %d: %+v", limit, vertex)
		}
	}
}

func TestResolveControlsOmitsEffortForStructuredThinkOff(t *testing.T) {
	modelinfo.SetThinkingRules([]modelinfo.ThinkingRule{{Match: []string{"gpt-oss"}, Style: string(modelinfo.ThinkStyleEffortLevels)}})
	t.Cleanup(func() { modelinfo.SetThinkingRules(nil) })

	provider := openaicompat.New("fireworks", "http://localhost", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/gpt-oss-20b", MaxTokens: 4096},
	})
	req := modelcall.CompletionRequest{
		Model:          "accounts/fireworks/models/gpt-oss-20b",
		Think:          modelcall.ThinkOff,
		ResponseFormat: CurateResponseFormat(),
	}
	body, err := provider.Prepare(req, false)
	testutil.FailErr(t, "prepare structured thinking", err)
	var built openaicompat.Request
	testutil.FailErr(t, "decode structured thinking", json.Unmarshal(body, &built))
	if built.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q want omitted for utility json_schema", built.ReasoningEffort)
	}
	if built.Thinking != nil {
		t.Fatalf("thinking = %+v want nil", built.Thinking)
	}
}

func streamReasoningServer(t *testing.T, frames []string, sent *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		*sent = decoded
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, frame := range frames {
			fmt.Fprintf(w, "data: %s\n\n", frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
}

func TestEffortLevelsYAMLAndProviderOverride(t *testing.T) {
	cfg, err := decodeProviderConfig([]byte(`
model_thinking:
  - match: [example-model]
    style: effort_levels
    effort_levels: {low: low, medium: high, high: max}
providers:
  - id: custom-gateway
    models:
      - id: example-model
        reasoning_effort_levels: {low: minimal, medium: medium, high: xhigh}
`))
	if err != nil {
		t.Fatalf("decode effort configuration: %v", err)
	}
	if err := modelinfo.ValidateThinkingRules(cfg.ModelThinking); err != nil {
		t.Fatalf("validate effort configuration: %v", err)
	}
	withThinkingRules(t, cfg.ModelThinking)
	entry := overlayLocalModel(modelinfo.Entry{ID: "example-model"}, cfg.Providers[0].Models[0])
	for _, profile := range []providerprofile.Profile{providerprofile.CloudflareWorkersAI(), providerprofile.Together(), providerprofile.Openrouter()} {
		provider := openaicompat.New("custom-gateway", "https://example.invalid/v1", "fixture", []modelinfo.Entry{entry}).WithProfile(profile)
		for level, want := range map[modelcall.ThinkLevel]string{modelcall.ThinkLow: "minimal", modelcall.ThinkMedium: "medium", modelcall.ThinkHigh: "xhigh"} {
			body, err := provider.Prepare(modelcall.CompletionRequest{Model: entry.ID, Think: level}, false)
			testutil.FailErr(t, "prepare configured effort", err)
			var controls openaicompat.Request
			testutil.FailErr(t, "decode configured effort", json.Unmarshal(body, &controls))
			got := controls.ReasoningEffort
			if controls.Reasoning != nil {
				got = controls.Reasoning.Effort
			}
			if got != want {
				t.Fatalf("style %s level %v = %q, want %q", profile.Thinking, level, got, want)
			}
		}
	}
}

func TestEffortLevelsRejectPartialOrInvalidYAML(t *testing.T) {
	for _, levels := range []string{
		"{low: low, high: max}",
		"{low: low, medium: high, high: 'invalid token'}",
		"{low: low, medium: high, high: max, highest: max}",
	} {
		_, err := decodeProviderConfig([]byte("providers:\n  - id: fixture\n    models:\n      - id: model\n        reasoning_effort_levels: " + levels))
		if err == nil {
			t.Fatalf("invalid model mapping accepted: %s", levels)
		}
		cfg, err := decodeProviderConfig([]byte("model_thinking:\n  - match: [model]\n    style: effort_levels\n    effort_levels: " + levels))
		if err == nil && modelinfo.ValidateThinkingRules(cfg.ModelThinking) == nil {
			t.Fatalf("invalid family mapping accepted: %s", levels)
		}
	}
}

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

// Hidden tokens are not an answer: reasoning followed by whitespace carries no
// payload.
func TestCompletionHasPayloadRejectsWhitespaceAndReasoningOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		completion *modelcall.Completion
		want       bool
	}{
		"nil":              {nil, false},
		"empty":            {&modelcall.Completion{}, false},
		"single space":     {&modelcall.Completion{Content: " "}, false},
		"newline and tabs": {&modelcall.Completion{Content: "\n\t \n"}, false},
		"reasoning only":   {&modelcall.Completion{Reasoning: "thought hard about it"}, false},
		"reasoning blocks": {&modelcall.Completion{ReasoningDetails: []json.RawMessage{json.RawMessage(`{}`)}}, false},
		"prose":            {&modelcall.Completion{Content: "here you go"}, true},
		"tool call only":   {&modelcall.Completion{ToolCalls: []api.ToolCall{{Name: "write"}}}, true},
		"space plus tool":  {&modelcall.Completion{Content: " ", ToolCalls: []api.ToolCall{{Name: "write"}}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := modelcall.CompletionHasPayload(tc.completion); got != tc.want {
				t.Fatalf("CompletionHasPayload = %v want %v", got, tc.want)
			}
		})
	}
}

func TestCompletionModelReasoningStampsProvenance(t *testing.T) {
	c := &modelcall.Completion{
		ProviderID: "openrouter-1",
		Model:      "moonshotai/kimi-k2.7-code",
		Reasoning:  "trace",
	}
	got := c.ModelReasoning()
	if got == nil || got.ProviderID != "openrouter-1" || got.Model != "moonshotai/kimi-k2.7-code" {
		t.Fatalf("ModelReasoning = %+v want the producing pair", got)
	}
	if (&modelcall.Completion{Content: "hi"}).ModelReasoning() != nil {
		t.Fatal("a turn with no trace must not mint an empty record")
	}
}

// A trace cannot be redacted in place: its blocks are signed byte-exact, so a
// substituted span produces a trace the provider rejects, and scrubbing Text
// while Details still holds the value would leak on the next turn.
func TestReasoningCarryingASecretIsDroppedWhole(t *testing.T) {
	msg := api.Message{
		Role:    api.MessageRoleAssistant,
		Content: "done",
		ModelReasoning: &api.ModelReasoning{
			ProviderID: "openrouter-1",
			Model:      "m",
			Text:       "the key is " + modelScreenGitHubToken,
			Details:    []json.RawMessage{json.RawMessage(`{"type":"reasoning.text","text":"` + modelScreenGitHubToken + `"}`)},
		},
	}
	out, changed := RedactMessageForStorage(context.Background(), modelScreenMatcher(t), msg)
	if !changed {
		t.Fatal("secret in reasoning must count as a redaction")
	}
	if out.ModelReasoning != nil {
		t.Fatalf("reasoning survived with a secret in it: %+v", out.ModelReasoning)
	}
	if out.HostSecretRedaction == nil || out.HostSecretRedaction.Occurrences() == 0 {
		t.Fatalf("redaction provenance = %+v want a non-zero count", out.HostSecretRedaction)
	}
}

func TestCleanReasoningSurvivesTheSecretScreen(t *testing.T) {
	msg := api.Message{
		Role:           api.MessageRoleAssistant,
		Content:        "done",
		ModelReasoning: &api.ModelReasoning{ProviderID: "p", Model: "m", Text: "nothing sensitive"},
	}
	out, changed := RedactMessageForStorage(context.Background(), modelScreenMatcher(t), msg)
	if changed {
		t.Fatal("clean message must not report a redaction")
	}
	if out.ModelReasoning == nil || out.ModelReasoning.Text != "nothing sensitive" {
		t.Fatalf("clean reasoning was dropped: %+v", out.ModelReasoning)
	}
}
