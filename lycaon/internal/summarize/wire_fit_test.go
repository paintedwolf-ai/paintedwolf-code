package summarize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// wireFitStructure builds candidates meaty enough that the default pack budget
// fills and the marshaled payload overshoots the wire budget.
func wireFitStructure(files int) []StructureCandidate {
	out := make([]StructureCandidate, files)
	for i := range out {
		var head strings.Builder
		var symbols []StructureSymbol
		for s := 0; s < 6; s++ {
			line := s*10 + 1
			symbols = append(symbols, StructureSymbol{
				Kind: "func", Name: fmt.Sprintf("HandleRequestVariant%dCase%d", i, s), Line: line,
			})
			for l := 0; l < 10; l++ {
				fmt.Fprintf(&head, "func body line %d of file %d with padding text\n", line+l, i)
			}
		}
		out[i] = StructureCandidate{
			RelPath: fmt.Sprintf("pkg/subsystem%d/handler_file_%d.go", i%6, i),
			Kind:    StructureKindFile, LineCount: 60,
			Head: head.String(), StartLine: 1,
			Symbols: symbols,
		}
	}
	return out
}

// wireFitEstimator excludes host-only diagnostics.
func wireFitEstimator(caps Caps, envelope int) WireFitEstimator {
	return func(r Result) int {
		pack := r.Pack
		pack.Gaps = nil
		raw, err := json.Marshal(pack)
		if err != nil {
			return 0
		}
		return caps.EstimateTokens(string(raw)) + envelope
	}
}

func TestWireFitTrimsUnderBudget(t *testing.T) {
	caps := DefaultCaps()
	structure := wireFitStructure(30)
	const envelope = 600

	// Precondition: with wire fit disabled the payload overshoots the wire budget.
	off := caps
	off.Pack.WireBudgetTokens = 0
	engOff := NewEngine(fakeGather{GatherResult{Mode: ModeRepo, Structure: structure}}, off)
	engOff.WireFit = wireFitEstimator(off, envelope)
	resOff, err := engOff.Run(context.Background(), Request{Task: "explain handlers", Path: "pkg"})
	if err != nil {
		t.Fatalf("run without wire fit: %v", err)
	}
	if resOff.Orchestration.Curator.WireFitPasses != 0 {
		t.Fatalf("wire_fit_passes = %d with wire fit disabled, want 0", resOff.Orchestration.Curator.WireFitPasses)
	}
	est := wireFitEstimator(off, envelope)(resOff)
	if est <= caps.Pack.WireBudgetTokens {
		t.Fatalf("fixture too small: estimate %d ≤ wire budget %d", est, caps.Pack.WireBudgetTokens)
	}

	eng := NewEngine(fakeGather{GatherResult{Mode: ModeRepo, Structure: structure}}, caps)
	eng.WireFit = wireFitEstimator(caps, envelope)
	res, err := eng.Run(context.Background(), Request{Task: "explain handlers", Path: "pkg"})
	if err != nil {
		t.Fatalf("run with wire fit: %v", err)
	}
	if res.Orchestration.Curator.WireFitPasses < 1 {
		t.Fatalf("wire_fit_passes = %d, want ≥ 1 on overshooting payload", res.Orchestration.Curator.WireFitPasses)
	}
	fitted := wireFitEstimator(caps, envelope)(res)
	if fitted > caps.Pack.WireBudgetTokens {
		t.Fatalf("fitted estimate %d exceeds wire budget %d after %d passes",
			fitted, caps.Pack.WireBudgetTokens, res.Orchestration.Curator.WireFitPasses)
	}
	if len(res.Pack.Identity) == 0 {
		t.Fatal("fitted pack lost identity tier")
	}
}
