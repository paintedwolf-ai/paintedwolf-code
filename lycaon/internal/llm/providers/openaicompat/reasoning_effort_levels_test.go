package openaicompat

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestGLMFlashGatewayUsesSupportedModelEfforts(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	model := "zai-org/glm-5.3-flash"
	provider := New("gateway", "https://example.invalid/v1", "fixture", nil).WithProfile(providerprofile.Openrouter())
	req := modelcall.CompletionRequest{Model: model, Tools: []tools.ToolMeta{{Name: "read"}}}
	for level, want := range map[modelcall.ThinkLevel]string{modelcall.ThinkUnset: "low", modelcall.ThinkOff: "low", modelcall.ThinkMedium: "low", modelcall.ThinkHigh: "high"} {
		req.Think = level
		controls := provider.resolveRequestControls(req, model, controlOpts{})
		if controls.Reasoning == nil || controls.Reasoning.Effort != want {
			t.Fatalf("gateway level %v = %+v, want %s", level, controls, want)
		}
	}
}
