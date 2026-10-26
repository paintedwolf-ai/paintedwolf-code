package openaicompat

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

func TestGLMFlashAliasWireEfforts(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	for _, route := range []struct {
		name    string
		model   string
		profile providerprofile.Profile
	}{
		{"fireworks", "accounts/fireworks/routers/glm-flash-latest", providerprofile.Fireworks()},
		{"litellm", "glm-flash-latest", providerprofile.Litellm()},
	} {
		t.Run(route.name, func(t *testing.T) {
			p := New("fixture", "https://example.invalid/v1", "fixture", nil).WithProfile(route.profile)
			for level, want := range map[modelcall.ThinkLevel]string{
				modelcall.ThinkUnset: "low", modelcall.ThinkOff: "low", modelcall.ThinkLow: "low",
				modelcall.ThinkMedium: "low", modelcall.ThinkHigh: "high",
			} {
				wire := encodeWire(t, p, modelcall.CompletionRequest{Model: route.model, Think: level})
				if wire["reasoning_effort"] != want {
					t.Fatalf("level %v effort = %v, want %s", level, wire["reasoning_effort"], want)
				}
			}
			caps := modelcall.ResolveThinkingCapabilities(route.profile, modelinfo.Entry{}, route.model)
			if caps.State != "supported" || caps.CanDisable || len(caps.Efforts) != 3 {
				t.Fatalf("alias capabilities = %+v", caps)
			}
		})
	}
}

func TestGLMFlashAliasPreservesExplicitControls(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	const model = "glm-flash-latest"
	p := New("fixture", "https://example.invalid/v1", "fixture", []modelinfo.Entry{{
		ID: model, ReasoningEffortLevels: modelinfo.ReasoningEffortLevels{Low: "low", Medium: "high", High: "max"},
	}}).WithProfile(providerprofile.Litellm())
	req := modelcall.CompletionRequest{Model: model, Think: modelcall.ThinkMedium}
	if wire := encodeWire(t, p, req); wire["reasoning_effort"] != "high" {
		t.Fatalf("model override lost: %v", wire["reasoning_effort"])
	}
	req.ThinkingOverride = &modelcall.ThinkingOverride{Effort: "max"}
	if wire := encodeWire(t, p, req); wire["reasoning_effort"] != "max" {
		t.Fatalf("fixed effort lost: %v", wire["reasoning_effort"])
	}
	p = New("fixture", "https://example.invalid/v1", "fixture", []modelinfo.Entry{{
		ID: model, Thinking: &modelinfo.ThinkingCapabilities{State: "unsupported"},
	}}).WithProfile(providerprofile.Litellm())
	req.ThinkingOverride = nil
	if wire := encodeWire(t, p, req); wire["reasoning_effort"] != nil {
		t.Fatalf("explicit refusal lost: %v", wire["reasoning_effort"])
	}
}
