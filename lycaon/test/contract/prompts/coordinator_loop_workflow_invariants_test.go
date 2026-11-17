package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestHumanInputPhasesLatchPendingOnEnter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for key, m := range workflowfixture.ContractAllResolvedManifests(t) {
		for _, phase := range workflowfixture.HumanInputPhases(m) {
			vars, err := workflowphases.ApplyPhaseOnEnter(ctx, workflowphases.PhaseEnterRequest{Manifest: m, PhaseID: phase.ID})
			if err != nil {
				t.Fatalf("manifest %q phase %q on_enter: %v", key, phase.ID, err)
			}
			if !scaffoldvars.HasPendingUserInput(vars) {
				t.Fatalf("manifest %q phase %q: expected pending user input after on_enter", key, phase.ID)
			}
		}
	}
}
