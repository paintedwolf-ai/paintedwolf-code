package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAnthropicAdaptiveThinkingUsesEffortWithoutBudget(t *testing.T) {
	p := New("anthropic", "https://api.anthropic.com/v1", "k", []modelinfo.Entry{{
		ID: "claude-opus-4-8", ThinkStyle: string(modelinfo.ThinkStyleAdaptive),
	}})
	req := p.Prepare(modelcall.CompletionRequest{Model: "claude-opus-4-8", Think: modelcall.ThinkMedium}, false)
	if req.Thinking == nil || req.Thinking.Type != "adaptive" || req.Thinking.Display != "omitted" {
		t.Fatalf("thinking = %+v", req.Thinking)
	}
	if req.Thinking.BudgetTokens != 0 {
		t.Fatalf("adaptive request carried budget_tokens: %+v", req.Thinking)
	}
	if req.OutputConfig == nil || req.OutputConfig.Effort != "medium" {
		t.Fatalf("output_config = %+v", req.OutputConfig)
	}
}

func TestAnthropicAdaptiveDefaultOnEmitsExplicitDisable(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{{
		Match: []string{"claude-sonnet-5"}, Style: string(modelinfo.ThinkStyleAdaptive), DefaultOn: true,
	}})
	p := New("anthropic", "https://api.anthropic.com/v1", "k", []modelinfo.Entry{{ID: "claude-sonnet-5"}})
	req := p.Prepare(modelcall.CompletionRequest{Model: "claude-sonnet-5", Think: modelcall.ThinkOff}, false)
	if req.Thinking == nil || req.Thinking.Type != "disabled" || req.Thinking.Display != "" {
		t.Fatalf("thinking = %+v, want explicit disabled", req.Thinking)
	}
	if req.OutputConfig != nil {
		t.Fatalf("disabled adaptive request carried output_config: %+v", req.OutputConfig)
	}
}

func TestAnthropicAdaptiveAlwaysOnOmitsDisable(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{{
		Match: []string{"claude-fable"}, Style: string(modelinfo.ThinkStyleAdaptive), AlwaysOn: true, DefaultOn: true,
	}})
	p := New("anthropic", "https://api.anthropic.com/v1", "k", []modelinfo.Entry{{ID: "claude-fable-5"}})
	req := p.Prepare(modelcall.CompletionRequest{Model: "claude-fable-5", Think: modelcall.ThinkOff}, false)
	if req.Thinking != nil {
		t.Fatalf("always-on adaptive request sent disable: %+v", req.Thinking)
	}
}

func TestAnthropicResponsePreservesSignedAndRedactedThinking(t *testing.T) {
	response := &Response{Content: []ContentBlock{
		{Type: "thinking", Thinking: "summary", Signature: "opaque-signature"},
		{Type: "redacted_thinking", Data: "opaque-redaction"},
		{Type: "text", Text: "answer"},
	}}
	out, err := mapAnthropicResponse(response)
	if err != nil {
		t.Fatalf("map response: %v", err)
	}
	if out.Reasoning != "summary" || len(out.ReasoningDetails) != 2 {
		t.Fatalf("completion = %+v", out)
	}
	var signed, redacted ContentBlock
	if err := json.Unmarshal(out.ReasoningDetails[0], &signed); err != nil {
		t.Fatalf("decode signed block: %v", err)
	}
	if err := json.Unmarshal(out.ReasoningDetails[1], &redacted); err != nil {
		t.Fatalf("decode redacted block: %v", err)
	}
	if signed.Signature != "opaque-signature" || redacted.Data != "opaque-redaction" {
		t.Fatalf("details = %+v / %+v", signed, redacted)
	}
}

func TestAnthropicReplayRequiresMatchingProviderAndModel(t *testing.T) {
	details := []json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"summary","signature":"sig"}`),
		json.RawMessage(`{"type":"redacted_thinking","data":"secret"}`),
	}
	message := api.Message{Role: api.MessageRoleAssistant, Content: "answer", ModelReasoning: &api.ModelReasoning{
		ProviderID: "anthropic", Model: "claude-opus-4-8", Details: details,
	}}
	_, matched := ProjectMessages([]api.Message{message}, nil, false, "", "anthropic", "claude-opus-4-8")
	if len(matched) != 1 || len(matched[0].Content) != 3 || matched[0].Content[0].Signature != "sig" || matched[0].Content[1].Data != "secret" {
		t.Fatalf("matched replay = %+v", matched)
	}
	_, switched := ProjectMessages([]api.Message{message}, nil, false, "", "anthropic", "claude-sonnet-5")
	if len(switched) != 1 || len(switched[0].Content) != 1 || switched[0].Content[0].Type != "text" {
		t.Fatalf("model-switched replay = %+v", switched)
	}
}
