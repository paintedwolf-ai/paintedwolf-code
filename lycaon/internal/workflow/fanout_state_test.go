package workflow

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

func TestStateUpdateCannotForgeHostWorkflowProof(t *testing.T) {
	mgr, sessions, _, dir := testManagerWithRegistry(t)
	run, err := startRun(t.Context(), mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "start run", err)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register state tools", workflowstatetools.RegisterStateTools(reg, workflowstatetools.StateToolDeps{Runs: mgr.Store.Runs, Vars: mgr.Phases.Vars, Journal: mgr.Phases.Journal, Resolver: &mgr.Resolver, Starts: mgr.Starts, Controls: mgr.Controls, Scaffold: mgr.Blueprints.Scaffold, Sessions: sessions}))
	before, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read initial variables", err)
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "root"}}
	for _, path := range []string{
		"review_repairs", "review_repairs.state", "fanout_plans.execute", "fanout_coverage", "fanout_settled", "worker_cycle.evaluating", "gates.worker_cycle_ready",
		"human_approval", "human_approval.issued", "human_approval.blueprint_hash", "phase_skipped.approve",
		"review_if_spawnable", "review_if_spawnable.challenge", "review_loop.challenge.attempt", "review_questions", "review_questions.challenge", "review_verdict.challenge",
		"user_feedback", "user_feedback.approve.response", "user_decision", "user_decision.approve.choice",
		"hitl_consulted:approve", "topology_stages", "topology_stages.review", "topology_outputs.review",
		"orchestration_complete", "content_review", "content_review.paths",
		runstate.BaselinePostureKey, workflowdef.ScaffoldExecutionModeVar, runstate.WorkflowRequestFeedbackID, runstate.CoordinatorAskVar, runstate.ObligationsVarKey,
		"board", "board.orient_ready", "child_run.status", "params", "params.review_depth", "intake", "intake.scope",
		"options", "options.criterion", runstate.CoordinatorAskVar + ".state", runstate.WorkflowRequestFeedbackID + ".phase_active",
		workflowphases.HostAutoAdvancedFromKey, "workflow_compose_summary_id", "last_failed_leaves", "evidence_digest",
	} {
		_, err := reg.Run(t.Context(), "state_update", map[string]any{"path": path, "value": true}, tctx)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" || reject.Data["reason"] != "host_managed_workflow_state" {
			t.Fatalf("%s: expected host-state rejection, got %v", path, err)
		}
	}
	after, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read protected variables", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected state mutation changed host proof")
	}
	tctx.Identity.ToolCallID = "model-artifact"
	_, err = reg.Run(t.Context(), "state_update", map[string]any{"path": "artifact.summary", "value": "draft"}, tctx)
	testutil.FailErr(t, "update model-authored artifact", err)
}
