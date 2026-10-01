package modelcall

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestReasoningPolicyConsistentAcrossAgentTurns(t *testing.T) {
	for _, req := range []CompletionRequest{
		{},
		{Tools: []tools.ToolMeta{{Name: "read"}}},
		{Debug: RequestDebug{ParentSessionID: "parent"}},
		{Tools: []tools.ToolMeta{{Name: "edit"}}, Debug: RequestDebug{ParentSessionID: "parent"}},
	} {
		if got := ResolveThinkLevel(req, "", false); got != ThinkMedium {
			t.Fatalf("agent reasoning = %v, want medium", got)
		}
	}
}

func TestReasoningPolicyExplicitExceptions(t *testing.T) {
	for _, tc := range []struct {
		req      CompletionRequest
		override string
		strict   bool
		want     ThinkLevel
	}{
		{CompletionRequest{Composition: CompositionHostUtility}, "", false, ThinkOff},
		{CompletionRequest{Think: ThinkOff}, "high", false, ThinkOff},
		{CompletionRequest{Think: ThinkHigh}, "low", false, ThinkHigh},
		{CompletionRequest{}, "low", false, ThinkLow},
		{CompletionRequest{Think: ThinkHigh}, "high", true, ThinkOff},
	} {
		if got := ResolveThinkLevel(tc.req, tc.override, tc.strict); got != tc.want {
			t.Fatalf("reasoning = %v, want %v", got, tc.want)
		}
	}
}

func TestThinkingTypePreservesEnabledEffort(t *testing.T) {
	for _, level := range []ThinkLevel{ThinkLow, ThinkMedium, ThinkHigh} {
		typ, send := level.ThinkingTypeValue()
		if !send || typ != "enabled" {
			t.Fatalf("level %v disabled reasoning: %q %v", level, typ, send)
		}
	}
	typ, send := ThinkOff.ThinkingTypeValue()
	if !send || typ != "disabled" {
		t.Fatal("explicit off was lost")
	}
}
