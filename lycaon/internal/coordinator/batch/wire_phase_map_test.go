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
