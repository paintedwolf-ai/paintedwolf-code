package workflow

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestGenericGateKitLeavesKnownToRegistry(t *testing.T) {
	for _, id := range genericGateKitStaticLeaves {
		if id == workflowdef.CompleteWhenGatesSatisfied {
			continue
		}
		if !workflowdef.IsKnownCompleteWhen(id) && !workflowdef.IsKnownGateLeaf(id) {
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
		if !IsGenericGateKitLeaf(id) {
			t.Fatalf("example %q not in generic gate kit", id)
		}
		if !workflowdef.IsKnownGateLeaf(id) && !workflowdef.IsKnownCompleteWhen(id) {
			t.Fatalf("example %q not recognized by vocabulary registry", id)
		}
	}
}

func TestInternalDomainGateLeavesNotGenericKit(t *testing.T) {
	for _, id := range internalDomainGateLeaves {
		if IsGenericGateKitLeaf(id) {
			t.Fatalf("internal leaf %q must not appear in generic gate kit", id)
		}
		if !workflowdef.IsKnownGateLeaf(id) && !workflowdef.IsKnownCompleteWhen(id) {
			t.Fatalf("internal leaf %q not registered (doc drift)", id)
		}
	}
}
