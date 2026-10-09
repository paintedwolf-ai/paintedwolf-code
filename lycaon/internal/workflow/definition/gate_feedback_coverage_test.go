package definition_test

import (
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"testing"
)

func TestWorkflowGateFeedbackCatalogMatchesReferences(t *testing.T) {
	refs, err := workflowdef.CollectManifestGatePredicates()
	testutil.FailErr(t, "CollectManifestGatePredicates", err)
	leaves := workflowdef.UniqueGateLeafIDs(refs)
	if len(leaves) == 0 {
		t.Fatal("expected gate leaves from shipped manifests")
	}
	catalog, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	leafSet := map[string]struct{}{}
	for _, leaf := range leaves {
		leafSet[leaf] = struct{}{}
	}
	for _, id := range catalog.GateIDs() {
		if _, ok := leafSet[id]; !ok {
			t.Fatalf("orphan gate feedback YAML for gate %q (not referenced by shipped manifests)", id)
		}
		emptyCtx := feedback.GateFeedbackContext(feedback.WorkflowEvaluationContext{}, nil)
		msg, err := catalog.RenderGateFeedback(t.Context(), id, emptyCtx)
		testutil.FailErr(t, "RenderGateFeedback empty "+id, err)
		if msg == "" {
			t.Fatalf("empty render for gate %q", id)
		}
		popCtx := feedback.GateFeedbackContext(feedback.WorkflowEvaluationContext{
			WorkflowID: "plan", CurrentPhase: "intake", FailedLeaves: []string{id},
		}, map[string]any{"plan_file_exists": false, "missing_sections": []string{"## Goal"}, "body_chars": 0})
		msgPop, err := catalog.RenderGateFeedback(t.Context(), id, popCtx)
		testutil.FailErr(t, "RenderGateFeedback populated "+id, err)
		if msgPop == "" {
			t.Fatalf("empty populated render for gate %q", id)
		}
	}
}
