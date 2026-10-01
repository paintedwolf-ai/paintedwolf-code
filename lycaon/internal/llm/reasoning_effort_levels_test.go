package llm

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
)

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
