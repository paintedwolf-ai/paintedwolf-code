package summarize

import (
	"context"
	"testing"
)

func TestInitialPackBudgetReservesEnvelope(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.WireBudgetTokens = 2400
	eng := NewEngine(fakeGather{GatherResult{Mode: ModeRepo}}, caps)
	eng.WireFit = func(Result) int { return 0 }
	eng.WireEnvelopeTokens = 400
	got := eng.initialPackBudget()
	want := caps.Pack.WireBudgetTokens - 400
	if got != want {
		t.Fatalf("initialPackBudget = %d want %d", got, want)
	}
}

func TestInitialPackBudgetDisabledWithoutWireFit(t *testing.T) {
	caps := DefaultCaps()
	eng := NewEngine(fakeGather{GatherResult{Mode: ModeRepo}}, caps)
	if got := eng.initialPackBudget(); got != 0 {
		t.Fatalf("initialPackBudget = %d want 0 without wire fit", got)
	}
}

func TestInitialPackBudgetClampsToConfiguredInput(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 800
	caps.Pack.WireBudgetTokens = 2400
	eng := NewEngine(fakeGather{GatherResult{Mode: ModeRepo}}, caps)
	eng.WireFit = func(Result) int { return 0 }
	eng.WireEnvelopeTokens = 100
	if got := eng.initialPackBudget(); got != 800 {
		t.Fatalf("initialPackBudget = %d want configured input 800", got)
	}
}

func TestRunUsesInitialPackBudgetWithWireEnvelope(t *testing.T) {
	caps := DefaultCaps()
	structure := wireFitStructure(30)
	eng := NewEngine(fakeGather{GatherResult{Mode: ModeRepo, Structure: structure}}, caps)
	eng.WireFit = wireFitEstimator(caps, 600)
	eng.WireEnvelopeTokens = 600
	res, err := eng.Run(context.Background(), Request{Task: "explain handlers", Path: "pkg"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Orchestration.Curator.BudgetTokensSpent > caps.Pack.InputBudgetTokens {
		t.Fatalf("budget_tokens_spent = %d exceeds input cap %d",
			res.Orchestration.Curator.BudgetTokensSpent, caps.Pack.InputBudgetTokens)
	}
}
