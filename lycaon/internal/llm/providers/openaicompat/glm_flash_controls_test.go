package openaicompat

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestGLMFlashUsesExplicitEffortForAgentTurns(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	for _, host := range []struct {
		model   string
		profile providerprofile.Profile
	}{
		{"zai-org/GLM-5.3-Flash", providerprofile.Together()},
		{"@cf/zai-org/glm-5.3-flash", providerprofile.CloudflareWorkersAI()},
		{"accounts/fireworks/models/glm-5p3-flash", providerprofile.Fireworks()},
		{"zai-org/GLM-5.3-Flash", providerprofile.OpenAI()},
	} {
		provider := New("fixture", "https://example.invalid/v1", "fixture", nil).WithProfile(host.profile)
		req := modelcall.CompletionRequest{Model: host.model, Tools: []tools.ToolMeta{{Name: "read"}}}
		controls := provider.resolveRequestControls(req, host.model, controlOpts{})
		if controls.ReasoningEffort != "low" || controls.Thinking != nil || controls.Reasoning != nil {
			t.Fatalf("%s agent effort left to provider default: %+v", host.model, controls)
		}
		req.Think = modelcall.ThinkHigh
		if got := provider.resolveRequestControls(req, host.model, controlOpts{}).ReasoningEffort; got != "high" {
			t.Fatalf("%s explicit high effort was lost: %q", host.model, got)
		}
	}
}

func TestExplicitOutputCapSurvivesOmittedReasoning(t *testing.T) {
	provider := New("fixture", "https://example.invalid/v1", "fixture", []modelinfo.Entry{{ID: "plain", ThinkStyle: "none"}})
	req := modelcall.CompletionRequest{Model: "plain", MaxTokens: 123, Tools: []tools.ToolMeta{{Name: "read"}}}
	for _, opts := range []controlOpts{{}, {reasoning: reasoningFallbackOmit}, {strictRetry: true}} {
		controls := provider.resolveRequestControls(req, req.Model, opts)
		if controls.MaxTokens != req.MaxTokens {
			t.Fatalf("explicit cap lost when reasoning omitted: %+v", controls)
		}
	}
}

func TestGLMFlashUtilityAndRecoveryKeepMinimumReasoning(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	model := "@cf/zai-org/glm-5.3-flash"
	provider := New("cloudflare", "https://example.invalid/v1", "fixture", nil).WithProfile(providerprofile.CloudflareWorkersAI())
	req := modelcall.CompletionRequest{Model: model, Think: modelcall.ThinkOff, ResponseFormat: &modelcall.ResponseFormat{Type: modelcall.ResponseFormatJSONObject}}
	if got := provider.resolveRequestControls(req, model, controlOpts{}).ReasoningEffort; got != "low" {
		t.Fatalf("always-on utility effort = %q, want explicit low", got)
	}
	if got := provider.resolveRequestControls(req, model, controlOpts{reasoning: reasoningFallbackOff}).ReasoningEffort; got != "" {
		t.Fatalf("always-on model received disable fallback: %q", got)
	}
}
