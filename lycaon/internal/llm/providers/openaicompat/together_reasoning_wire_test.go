package openaicompat

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

func TestTogetherHybridReasoningUsesEnabled(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"qwen3"}, Style: string(modelinfo.ThinkStyleBooleanThink)},
	})
	const model = "Qwen/Qwen3.5-9B"
	p := New("together-1", "https://api.together.xyz/v1", "key", []modelinfo.Entry{{ID: model}}).
		WithProfile(providerprofile.Together())
	for _, tc := range []struct {
		name  string
		level modelcall.ThinkLevel
		on    bool
	}{
		{"application default", modelcall.ThinkUnset, true},
		{"off", modelcall.ThinkOff, false},
		{"low", modelcall.ThinkLow, true},
		{"medium", modelcall.ThinkMedium, true},
		{"high", modelcall.ThinkHigh, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := encodeWire(t, p, modelcall.CompletionRequest{Model: model, Think: tc.level})
			if !reflect.DeepEqual(wire["reasoning"], map[string]any{"enabled": tc.on}) {
				t.Fatalf("reasoning = %#v, want only enabled:%v", wire["reasoning"], tc.on)
			}
			for _, key := range []string{"reasoning_effort", "thinking", "think"} {
				if _, sent := wire[key]; sent {
					t.Fatalf("hybrid request included unsupported %s: %v", key, wire)
				}
			}
		})
	}
}

func TestTogetherAlwaysOnHybridDoesNotSendDisabled(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"hybrid"}, Style: string(modelinfo.ThinkStyleBooleanThink), AlwaysOn: true},
	})
	p := New("together-1", "https://api.together.xyz/v1", "key", []modelinfo.Entry{{ID: "hybrid"}}).
		WithProfile(providerprofile.Together())
	wire := encodeWire(t, p, modelcall.CompletionRequest{Model: "hybrid", Think: modelcall.ThinkOff})
	if _, sent := wire["reasoning"]; sent {
		t.Fatalf("always-on model received a disable control: %v", wire)
	}
}

func TestTogetherUtilityCallDisablesReasoning(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"qwen3"}, Style: string(modelinfo.ThinkStyleBooleanThink)},
	})
	p := New("together-1", "https://api.together.xyz/v1", "key", []modelinfo.Entry{
		{ID: "Qwen/Qwen3.5-9B"},
	}).WithProfile(providerprofile.Together())

	wire := encodeWire(t, p, modelcall.CompletionRequest{
		Model:     "Qwen/Qwen3.5-9B",
		Think:     modelcall.ThinkOff,
		MaxTokens: 200,
	})

	if _, ok := wire["thinking"]; ok {
		t.Fatalf("Together must not receive a family-specific thinking field: %v", wire)
	}
	if _, ok := wire["reasoning_effort"]; ok {
		t.Fatalf("Together utility call must not receive reasoning_effort: %v", wire)
	}
	reasoning, ok := wire["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("Together utility call is missing reasoning object: %v", wire)
	}
	if reasoning["enabled"] != false {
		t.Fatalf("reasoning = %v want enabled:false", reasoning)
	}
}
