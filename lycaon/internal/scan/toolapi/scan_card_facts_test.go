package toolapi

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompletedScanStatesItsFindingsBySeverity(t *testing.T) {
	out := &tools.ToolInvocationOut{}
	tctx := tools.ToolContext{Effects: tools.InvocationEffects{Out: out}}
	stateFindings(tctx, api.CodeScan{Status: api.CodeScanStatusComplete, FindingsCount: 9,
		FindingsByLevel: map[string]int{"critical": 1, "high": 2, "medium": 3, "low": 1, "info": 2}})
	want := api.ActionFact{Kind: api.ActionFactFound, Count: 9, Noun: "findings", High: 3, Medium: 3, Low: 1}
	if len(out.CardFacts) != 1 || out.CardFacts[0].Kind != want.Kind || out.CardFacts[0].Count != 9 || out.CardFacts[0].High != 3 || out.CardFacts[0].Medium != 3 || out.CardFacts[0].Low != 1 {
		t.Fatalf("card facts %+v, want %+v", out.CardFacts, want)
	}
	running := &tools.ToolInvocationOut{}
	stateFindings(tools.ToolContext{Effects: tools.InvocationEffects{Out: running}}, api.CodeScan{Status: api.CodeScanStatusRunning, FindingsCount: 4})
	if len(running.CardFacts) != 0 {
		t.Fatalf("a running scan stated findings: %+v", running.CardFacts)
	}
}
