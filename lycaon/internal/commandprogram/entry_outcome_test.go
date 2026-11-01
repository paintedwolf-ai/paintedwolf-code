package commandprogram

import "testing"

func TestEntryRefusalsSettleWithoutDispatchOrSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		remove       func(*EvaluationPorts)
		state        string
		status       int
	}{
		{"publication", "echo before; echo data > output", func(p *EvaluationPorts) { p.Publication = nil }, "rejected", 2},
		{"input", "echo before; cat < input.txt", func(p *EvaluationPorts) { p.Input = nil }, "rejected", 2},
		{"source", "echo before; source input.txt", func(p *EvaluationPorts) { p.Sources = nil }, "rejected", 2},
		{"jobs", "echo before; echo later &", func(p *EvaluationPorts) { p.Supervision = nil }, "rejected", 2},
		{"binder", "echo before", func(p *EvaluationPorts) { p.Binder = nil }, "failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t)
			tc.remove(&f.ports)
			outcome, err := f.run(t, t.Context(), tc.source)
			settlement := outcome.Settlement("fixture")
			if err == nil || outcome.Stop == nil || outcome.Status != tc.status || settlement.State != tc.state || settlement.Status == nil || *settlement.Status != tc.status || len(f.log.argv()) != 0 {
				t.Fatalf("entry failure became execution or success: %+v / %+v / %v", outcome, settlement, err)
			}
		})
	}
}
