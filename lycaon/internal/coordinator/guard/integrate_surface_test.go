package guard_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
)

func TestOverlayIntegratePending_ledgerOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		state surface.ImplementSessionState
		want  bool
	}{
		{
			name:  "ledger_pending",
			state: surface.ImplementSessionState{PendingOverlayIDs: []string{"job-a"}},
			want:  true,
		},
		{
			name:  "ledger_empty_ignores_open_summary_and_integrate_phase",
			state: surface.ImplementSessionState{PendingOverlayIDs: []string{}, BatchPhase: batch.PhaseIntegrate},
			want:  false,
		},
		{
			name:  "no_pending_no_history",
			state: surface.ImplementSessionState{BatchPhase: batch.PhaseIntegrate},
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := guard.OverlayIntegratePending(tc.state)
			if got != tc.want {
				t.Fatalf("OverlayIntegratePending = %v want %v", got, tc.want)
			}
		})
	}
}

func TestOverlayIntegrateRejectEndsToolLoop(t *testing.T) {
	t.Parallel()

	state := surface.ImplementSessionState{PendingOverlayIDs: []string{}}

	if !guard.OverlayIntegrateRejectEndsToolLoop("OVERLAY_PROMOTE_NOT_FOUND", state) {
		t.Fatal("expected stale overlay reject to end tool loop when ledger is clear")
	}
	if !guard.OverlayIntegrateRejectEndsToolLoop("WORKER_CANCEL_TERMINAL", state) {
		t.Fatal("expected worker cancel terminal reject to end tool loop when ledger is clear")
	}
	if guard.OverlayIntegrateRejectEndsToolLoop("OVERLAY_PROMOTE_NOT_FOUND", surface.ImplementSessionState{PendingOverlayIDs: []string{"job-a"}}) {
		t.Fatal("expected pending ledger to keep tool loop open")
	}
	if guard.OverlayIntegrateRejectEndsToolLoop("READ_NOT_FOUND", state) {
		t.Fatal("expected unrelated reject to keep tool loop open")
	}
}
