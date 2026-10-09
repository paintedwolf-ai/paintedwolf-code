package workflow

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRegistryGateEvaluatorGatesList(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	eval := RegistryGateEvaluator{Registry: reg}
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "ship",
			CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
			Gates: []string{
				"human_approval",
				"delegation_closeout_complete",
				"evidence_passed:verify",
			},
		}},
	}
	run := &api.WorkflowRun{CurrentPhase: "ship", SessionID: "s1", BlueprintPath: "p1"}
	ok, result, err := eval.PhaseGateMet(context.Background(), manifest, run, nil)
	testutil.FailErr(t, "eval.PhaseGateMet failed", err)
	if ok {
		t.Fatal("expected partial gate failure")
	}
	if len(result.FailedLeaves) == 0 {
		t.Fatalf("result = %+v", result)
	}

	const planBody = conditions.TestPlanContentWithTasks
	blueprintPath := settingsoverlay.Rel("blueprints/plan.md")
	deps := conditions.TestRegistryDepsWithEvidence()
	deps.BlueprintGet = func(_ context.Context, path string) (*api.Blueprint, error) {
		return &api.Blueprint{Path: path, Content: planBody}, nil
	}
	deps.BlueprintContent = func(_ context.Context, _, relPath string) (string, error) {
		if relPath != blueprintPath {
			return "", os.ErrNotExist
		}
		return planBody, nil
	}
	regPass, err := conditions.NewDefaultRegistry(deps)
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	evalPass := RegistryGateEvaluator{Registry: regPass}
	vars := runstate.SetHumanApprovalIssued(nil, true)
	vars = runstate.SetHumanApprovalReady(vars, true)
	vars = runstate.SetHostVar(vars, "human_approval.blueprint_path", blueprintPath)
	vars = runstate.SetHumanApprovalHash(vars, workflowdef.HashBlueprintContent(planBody))
	vars = runstate.SetGateSatisfied(vars, "delegation_closeout_complete", true)
	vars = runstate.SetGateSatisfied(vars, "evidence_passed:verify", true)
	ok, result, err = evalPass.PhaseGateMet(context.Background(), manifest, run, vars)
	if err != nil || !ok {
		t.Fatalf("all gates should pass: ok=%v result=%+v err=%v", ok, result, err)
	}
}

func TestRegistryGateEvaluatorCompoundCompleteWhen(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	eval := RegistryGateEvaluator{Registry: reg}
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "implement",
			CompleteWhen: "delegation_closeout_complete and evidence_passed:verify",
		}},
	}
	run := &api.WorkflowRun{CurrentPhase: "implement", SessionID: "s1"}
	ok, result, err := eval.PhaseGateMet(context.Background(), manifest, run, nil)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(result.FailedLeaves) == 0 {
		t.Fatalf("failed_leaves = %v", result.FailedLeaves)
	}
}

func TestCollectFailedLeavesUnknownIgnored(t *testing.T) {
	reg := conditions.NewRegistry()
	ec := conditions.EvalContext{}
	failed, err := collectFailedLeaves(reg, ec, []string{"missing_leaf"})
	testutil.FailErr(t, "collectFailedLeaves failed", err)
	if len(failed) != 1 || failed[0] != "missing_leaf" {
		t.Fatalf("failed = %v", failed)
	}
}
