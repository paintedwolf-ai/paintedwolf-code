package anthropic

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func TestFixedAnthropicBudgetAndDisableWire(t *testing.T) {
	budget, off := 4096, false
	o := modelcall.ThinkingOverride{Mode: "fixed", BudgetTokens: &budget}
	anthropic := Request{MaxTokens: 8192}
	applyFixedAnthropicThinking(&anthropic, modelcall.ModelThinking{Style: modelinfo.ThinkStyleBudgetTokens}, o)
	if anthropic.Thinking.BudgetTokens != budget || anthropic.MaxTokens != 8192 {
		t.Fatalf("budget changed: %+v", anthropic)
	}
	applyFixedAnthropicThinking(&anthropic, modelcall.ModelThinking{Style: modelinfo.ThinkStyleAdaptive}, modelcall.ThinkingOverride{Mode: "fixed", Effort: "max"})
	if anthropic.Thinking.Type != "adaptive" || anthropic.OutputConfig.Effort != "max" {
		t.Fatalf("adaptive control lost: %+v", anthropic)
	}
	applyFixedAnthropicThinking(&anthropic, modelcall.ModelThinking{Style: modelinfo.ThinkStyleAdaptive}, modelcall.ThinkingOverride{Mode: "fixed", Enabled: &off})
	if anthropic.Thinking.Type != "disabled" || anthropic.OutputConfig != nil {
		t.Fatal("disabled thinking retained adaptive effort")
	}

}
