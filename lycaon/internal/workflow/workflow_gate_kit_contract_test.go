package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestGenericGateKitLeavesKnownToRegistry(t *testing.T) {
	for _, id := range []string{"orchestration_complete", "child_run_complete", "human_approval"} {
		if id == workflowdef.CompleteWhenGatesSatisfied {
			continue
		}
		if !workflowdef.IsKnownCompleteWhen(id) && !vocabulary.IsPredicate(id) {
			t.Fatalf("generic gate kit leaf %q not recognized by vocabulary registry", id)
		}
	}
	examples := []string{
		"var_equals:doc.status,approved",
		"var_truthy:doc.ready",
		"user_decision_received:approve",
		"user_feedback_received:clarify",
		"user_decision:approve,approve",
		"evidence_passed:verify",
		"phase_is:clarify",
		"phase_skipped:research",
	}
	for _, id := range examples {
		if !vocabulary.IsOverlayLeaf(id) {
			t.Fatalf("example %q not in generic gate kit", id)
		}
		if !vocabulary.IsPredicate(id) && !workflowdef.IsKnownCompleteWhen(id) {
			t.Fatalf("example %q not recognized by vocabulary registry", id)
		}
	}
}

func TestInternalDomainGateLeavesNotGenericKit(t *testing.T) {
	for _, id := range []string{"plan_stub_valid", "research_satisfied", "delivery_gates_passed", "fanout_planned"} {
		if vocabulary.IsOverlayLeaf(id) {
			t.Fatalf("internal leaf %q must not appear in generic gate kit", id)
		}
		if !vocabulary.IsPredicate(id) && !workflowdef.IsKnownCompleteWhen(id) {
			t.Fatalf("internal leaf %q not registered (doc drift)", id)
		}
	}
}
