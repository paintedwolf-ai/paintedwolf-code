package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
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

func TestOmittedThinkingRoundTripRetainsThinkingField(t *testing.T) {
	completion, err := mapAnthropicResponse(&Response{Content: []ContentBlock{
		{Type: "thinking", Thinking: "", Signature: "sig-omitted"},
		{Type: "text", Text: "answer"},
	}})
	if err != nil {
		t.Fatalf("map response: %v", err)
	}
	if len(completion.ReasoningDetails) != 1 || !strings.Contains(string(completion.ReasoningDetails[0]), `"thinking":""`) {
		t.Fatalf("completion reasoning details = %s", completion.ReasoningDetails)
	}
	var block ContentBlock
	if err := json.Unmarshal(completion.ReasoningDetails[0], &block); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	raw, err := surveyjson.Marshal(block)
	if err != nil {
		t.Fatalf("marshal block: %v", err)
	}
	if !strings.Contains(string(raw), `"thinking":""`) || !strings.Contains(string(raw), `"signature":"sig-omitted"`) {
		t.Fatalf("round-tripped block = %s", raw)
	}
}
