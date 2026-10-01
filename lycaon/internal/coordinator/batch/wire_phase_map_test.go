package batch_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToWirePhase(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{batch.PhaseDispatch, string(api.CoordinatorBatchPhaseDispatch)},
		{batch.PhaseIntegrate, string(api.CoordinatorBatchPhaseIntegrate)},
		{batch.PhaseSynthesize, string(api.CoordinatorBatchPhaseSynthesize)},
		{batch.PhaseClosed, string(api.CoordinatorBatchPhaseClosed)},
		{batch.PhasePreDispatch, ""},
		{"", ""},
		{"unknown", ""},
	}
	for _, tc := range tests {
		if got := batch.ToWirePhase(tc.in); got != tc.want {
			t.Fatalf("ToWirePhase(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestHostPhasesCoverMachineConsts(t *testing.T) {
	want := map[string]struct{}{
		batch.PhasePreDispatch: {},
		batch.PhaseDispatch:    {},
		batch.PhaseIntegrate:   {},
		batch.PhaseSynthesize:  {},
		batch.PhaseClosed:      {},
	}
	for _, p := range batch.HostPhases() {
		if _, ok := want[p]; !ok {
			t.Fatalf("unexpected host phase %q in map", p)
		}
		delete(want, p)
	}
	if len(want) > 0 {
		t.Fatalf("host phases missing from map: %v", want)
	}
}

// Every wire phase must map from a host phase.
func TestWirePhasesMatchAPIConsts(t *testing.T) {
	want := map[string]struct{}{}
	for _, v := range api.AllCoordinatorBatchPhaseValues() {
		want[string(v)] = struct{}{}
	}
	for _, w := range batch.WirePhases() {
		if _, ok := want[w]; !ok {
			t.Fatalf("unexpected wire phase %q", w)
		}
		delete(want, w)
	}
	if len(want) > 0 {
		t.Fatalf("wire phases missing: %v", want)
	}
}
