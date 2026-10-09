package gates_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFailClosedGateEvaluatorBlocksPackPredicate(t *testing.T) {
	eval := workflowgates.FailClosedGateEvaluator{}
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "stub", CompleteWhen: "plan_stub_valid", Next: "approve"},
		},
	}
	run := &api.WorkflowRun{CurrentPhase: "stub"}
	ok, result, err := eval.PhaseGateMet(context.Background(), manifest, run, nil)
	testutil.FailErr(t, "eval.PhaseGateMet failed", err)
	if ok {
		t.Fatal("expected domain predicate to block advance")
	}
	if result.Reason != "plan_stub_valid" {
		t.Fatalf("reason = %q", result.Reason)
	}
}
