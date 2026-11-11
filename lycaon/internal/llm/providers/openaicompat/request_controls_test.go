package openaicompat

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestResolveControlsOrchestrationTurn(t *testing.T) {
	temp := 0.6
	provider := New("fireworks", "http://localhost", "key", []modelinfo.Entry{
		{ID: "discovered-model", Temperature: &temp, MaxTokens: 16384},
	})
	req := modelcall.CompletionRequest{
		Model: "accounts/fireworks/models/kimi-k2p7-code",
		Tools: []tools.ToolMeta{{Name: "update_progress"}},
		Debug: modelcall.RequestDebug{Surface: toolcontract.SurfaceImplementInvestigate, SessionID: "s1"},
	}
	built := provider.resolveRequestControls(req, req.Model, controlOpts{})
	if built.ReasoningEffort != "medium" {
		t.Fatalf("reasoning_effort = %q", built.ReasoningEffort)
	}
	if built.MaxTokens != modelcall.OrchestrationMaxTokens+8192 {
		t.Fatalf("max_tokens = %d want orchestration cap %d", built.MaxTokens, modelcall.OrchestrationMaxTokens)
	}
}

func TestResolveControlsStrictSessionBudget(t *testing.T) {
	t.Cleanup(modelcall.ResetTurnBudgetForTest)
	modelcall.MarkSessionReasoningRecovery("strict-session")

	provider := New("fireworks", "http://localhost", "key", []modelinfo.Entry{
		{ID: "m", MaxTokens: 16384, ReasoningEffort: "medium"},
	})
	req := modelcall.CompletionRequest{
		Model: "m",
		Tools: []tools.ToolMeta{{Name: "task"}},
		Debug: modelcall.RequestDebug{Surface: "implement_synthesis", SessionID: "strict-session"},
	}
	built := provider.resolveRequestControls(req, req.Model, controlOpts{})
	if built.MaxTokens != 16384 {
		t.Fatalf("max_tokens = %d want answer cap %d", built.MaxTokens, 16384)
	}
	if built.ReasoningEffort != modelcall.MinimumReasoningEffort {
		t.Fatalf("reasoning_effort = %q", built.ReasoningEffort)
	}
}

func TestResolveControlsOrchestrationGivesThinkingRoom(t *testing.T) {
	t.Cleanup(modelcall.ResetTurnBudgetForTest)
	const configured = 12288
	provider := New("p", "http://localhost", "key", []modelinfo.Entry{
		{ID: "m", MaxTokens: configured},
	})
	req := modelcall.CompletionRequest{
		Model: "m",
		Tools: []tools.ToolMeta{{Name: "task"}},
		Debug: modelcall.RequestDebug{Surface: toolcontract.SurfaceImplementDispatch, SessionID: "room"},
	}
	if built := provider.resolveRequestControls(req, req.Model, controlOpts{}); built.MaxTokens != configured {
		t.Fatalf("orchestration max_tokens = %d, want configured %d (room preserved)", built.MaxTokens, configured)
	}

	modelcall.MarkSessionReasoningRecovery("room")
	if built := provider.resolveRequestControls(req, req.Model, controlOpts{}); built.MaxTokens != configured {
		t.Fatalf("strict max_tokens = %d, want configured cap %d", built.MaxTokens, configured)
	}
}

func TestResolveControlsSkipsReasoningWhenUnsupported(t *testing.T) {
	t.Cleanup(ResetReasoningProbeForTest)
	markReasoningFallback("openai-compatible", "gpt-4o", reasoningFallbackOmit)

	provider := New("openai-compatible", "http://localhost", "key", nil)
	req := modelcall.CompletionRequest{
		Model: "gpt-4o",
		Tools: []tools.ToolMeta{{Name: "task"}},
		Debug: modelcall.RequestDebug{Surface: toolcontract.SurfaceImplementDispatch},
	}
	built := provider.resolveRequestControls(req, req.Model, controlOpts{})
	if built.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q want omitted after probe", built.ReasoningEffort)
	}
}
