package workflow

import (
	"slices"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestDecomposeGateExpressionCompositeAndParameterized(t *testing.T) {
	leaves := decomposeGateExpression("delegation_closeout_complete and evidence_passed:verify")
	want := []string{"delegation_closeout_complete", "evidence_passed:verify"}
	if !slices.Equal(leaves, want) {
		t.Fatalf("leaves = %v want %v", leaves, want)
	}
	if got := decomposeGateExpression("gate_satisfied:plan_stub_valid"); !slices.Equal(got, []string{"plan_stub_valid"}) {
		t.Fatalf("gate_satisfied prefix = %v", got)
	}
	if got := decomposeGateExpression("plan_stub_valid"); !slices.Equal(got, []string{"plan_stub_valid"}) {
		t.Fatalf("single leaf = %v", got)
	}
}

func TestCollectManifestGatePredicatesPlanCatalog(t *testing.T) {
	refs, err := CollectManifestGatePredicates()
	if err != nil {
		t.Fatalf("CollectManifestGatePredicates: %v", err)
	}
	var planLeaves []string
	for _, ref := range refs {
		if ref.ManifestID != "plan" {
			continue
		}
		planLeaves = append(planLeaves, ref.LeafID)
	}
	for _, id := range []string{"plan_stub_valid", "research_satisfied", "human_approval", "child_run_complete"} {
		if !slices.Contains(planLeaves, id) {
			t.Fatalf("plan manifest missing leaf %q; got %v", id, planLeaves)
		}
	}
}

func TestGatePredicatesIncludeChildCompletionContract(t *testing.T) {
	manifest := workflowdef.Manifest{ID: "child-contract", PhaseDefs: []workflowdef.PhaseDef{{
		ID:                "work",
		CompleteWhen:      "worker_cycle_ready",
		Gates:             []string{"topology_stage_complete"},
		ChildCompleteWhen: "delivery_gates_passed or closeout_gates_passed",
		ChildGates:        []string{"evidence_passed:verify"},
	}}}
	refs := gatePredicatesFromManifest(manifest)
	var leaves []string
	for _, ref := range refs {
		leaves = append(leaves, ref.LeafID)
	}
	for _, id := range []string{
		"worker_cycle_ready",
		"topology_stage_complete",
		"delivery_gates_passed",
		"closeout_gates_passed",
		"evidence_passed:verify",
	} {
		if !slices.Contains(leaves, id) {
			t.Fatalf("child-aware manifest refs missing %q; got %v", id, leaves)
		}
	}
}
