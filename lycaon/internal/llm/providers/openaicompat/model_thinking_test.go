package openaicompat

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOpenAIDriverEmitsThinkingTypeForKimiOnAggregator(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))

	p := New("fireworks", "https://example.invalid/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p6"},
	}).WithProfile(providerprofile.Fireworks())

	req := modelcall.CompletionRequest{Model: "accounts/fireworks/models/kimi-k2p6", Tools: []tools.ToolMeta{{Name: "read"}}}
	controls := p.resolveRequestControls(req, req.Model, controlOpts{})
	if controls.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q want empty for a thinking_type model", controls.ReasoningEffort)
	}
	if controls.Thinking == nil || controls.Thinking.Type != "enabled" {
		t.Fatalf("thinking = %+v want enabled", controls.Thinking)
	}

	p = New("fireworks", "https://example.invalid/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p7-code"},
	}).WithProfile(providerprofile.Fireworks())
	req = modelcall.CompletionRequest{Model: "accounts/fireworks/models/kimi-k2p7-code", Tools: []tools.ToolMeta{{Name: "read"}}}
	controls = p.resolveRequestControls(req, req.Model, controlOpts{})
	if controls.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q want empty for a thinking_type model", controls.ReasoningEffort)
	}
	if controls.Thinking == nil || controls.Thinking.Type != "enabled" {
		t.Fatalf("k2p7-code thinking = %+v want enabled", controls.Thinking)
	}
}
