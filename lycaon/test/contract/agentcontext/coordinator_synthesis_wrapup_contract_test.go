package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorSynthesisSurfaceReadOnlyTools(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	synthesis := surfaces["implement_synthesis"]
	for _, forbidden := range []string{
		"write", "edit", "task", "pack_board", "wait",
		"worker_cancel", "answer_decision", "extend_worker_budget", "decline_worker_budget",
		"promote_overlay", "reject_overlay", "preview_overlay",
	} {
		if contractcheck.ContainsString(synthesis, forbidden) {
			t.Fatalf("implement_synthesis must not expose %q", forbidden)
		}
	}
	if !contractcheck.ContainsString(synthesis, "update_progress") {
		t.Fatal("implement_synthesis must expose update_progress for checklist reconciliation")
	}
	for _, required := range []string{"command", "scan_pack", "render_view"} {
		if !contractcheck.ContainsString(synthesis, required) {
			t.Fatalf("implement_synthesis missing survey tool %q", required)
		}
	}
}
