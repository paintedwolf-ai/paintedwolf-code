package phases_test

import (
	workflow "github.com/lycaon/lycaon/internal/workflow"

	"context"
	"encoding/json"
	"errors"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"testing"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func registerTransitionToolTestRegistry(t *testing.T, mgr *workflow.RunManager) *tools.DefaultRegistry {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	if err := workflowphases.RegisterTransitionTool(reg, mgr.Phases); err != nil {
		testutil.FailErr(t, "workflowphases.RegisterTransitionTool", err)
	}
	return reg
}

func TestWorkflowTransitionHappyPath(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	reg := registerTransitionToolTestRegistry(t, mgr)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	tctx := toolContext("coordinator", sess.ID, sess.WorkspacePath)
	tctx.ToolCallID = "transition-call-1"
	out, err := reg.Run(ctx, "workflow_transition", map[string]any{"transition_id": "critique"}, tctx)
	testutil.FailErr(t, "workflow_transition critique", err)
	var result workflowphases.TransitionToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		testutil.FailErr(t, "unmarshal", err)
	}
	if result.Run == nil || result.Run.CurrentPhase != "review" {
		t.Fatalf("result = %+v want phase review", result)
	}
	replayed, err := reg.Run(ctx, "workflow_transition", map[string]any{"transition_id": "critique"}, tctx)
	testutil.FailErr(t, "replay workflow_transition", err)
	var replayedResult workflowphases.TransitionToolResult
	testutil.FailErr(t, "unmarshal replay", json.Unmarshal([]byte(replayed), &replayedResult))
	if replayedResult.Run == nil || replayedResult.Run.Revision != result.Run.Revision {
		t.Fatalf("replayed result = %+v want revision %d", replayedResult, result.Run.Revision)
	}
}

func TestWorkflowTransitionEitherArm(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	reg := registerTransitionToolTestRegistry(t, mgr)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	out, err := reg.Run(ctx, "workflow_transition", map[string]any{"transition_id": "deepen"}, toolContext("coordinator", sess.ID, sess.WorkspacePath))
	testutil.FailErr(t, "deepen", err)
	var result workflowphases.TransitionToolResult
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &result))
	if result.Run == nil || result.Run.CurrentPhase != "research_more" {
		t.Fatalf("result = %+v", result)
	}
}

func TestWorkflowTransitionActorDenied(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	reg := registerTransitionToolTestRegistry(t, mgr)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	_, err = reg.Run(ctx, "workflow_transition", map[string]any{"transition_id": "side_quest"}, toolContext("coordinator", sess.ID, sess.WorkspacePath))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKFLOW_TRANSITION_ACTOR_DENIED" {
		t.Fatalf("err = %v want WORKFLOW_TRANSITION_ACTOR_DENIED", err)
	}
}

func TestWorkflowTransitionUnknownID(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	reg := registerTransitionToolTestRegistry(t, mgr)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	_, err = reg.Run(ctx, "workflow_transition", map[string]any{"transition_id": "nope"}, toolContext("coordinator", sess.ID, sess.WorkspacePath))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKFLOW_TRANSITION_UNKNOWN" {
		t.Fatalf("err = %v want WORKFLOW_TRANSITION_UNKNOWN", err)
	}
}

func TestWorkflowTransitionT36ApproveAwaitingDoesNotBlock(t *testing.T) {
	mgr, sessStore, dir := choiceTransitionsTestMgr(t)
	reg := registerTransitionToolTestRegistry(t, mgr)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetHostVar(vars, "human_approval.active", true)
	vars = runstate.SetHostVar(vars, "human_approval.ready", true)
	testutil.FailErr(t, "UpdateVars", mgr.Store.State.UpdateVars(ctx, run, dir, vars))
	if !scaffoldvars.HumanApprovalAwaiting(vars) {
		t.Fatal("expected human_approval_awaiting")
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		t.Fatal("human_approval_awaiting must not trip HasPendingUserInput")
	}

	out, err := reg.Run(ctx, "workflow_transition", map[string]any{"transition_id": "critique"}, toolContext("coordinator", sess.ID, sess.WorkspacePath))
	testutil.FailErr(t, "workflow_transition while awaiting approval", err)
	var result workflowphases.TransitionToolResult
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &result))
	if result.Run == nil || result.Run.CurrentPhase != "review" {
		t.Fatalf("result = %+v", result)
	}
}

func TestWorkflowTransitionPendingFeedbackReusesHint(t *testing.T) {
	mgr, sessStore, dir := choiceTransitionsTestMgr(t)
	reg := registerTransitionToolTestRegistry(t, mgr)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = withPendingFeedback(vars, "decide", "Which path?")
	testutil.FailErr(t, "UpdateVars", mgr.Store.State.UpdateVars(ctx, run, dir, vars))

	_, err = reg.Run(ctx, "workflow_transition", map[string]any{"transition_id": "critique"}, toolContext("coordinator", sess.ID, sess.WorkspacePath))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKFLOW_FEEDBACK_PENDING" {
		t.Fatalf("err = %v want WORKFLOW_FEEDBACK_PENDING", err)
	}
}

func TestWorkflowTransitionInactive(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	reg := registerTransitionToolTestRegistry(t, mgr)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	_, err = reg.Run(ctx, "workflow_transition", map[string]any{"transition_id": "critique"}, toolContext("coordinator", sess.ID, sess.WorkspacePath))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKFLOW_TRANSITION_INACTIVE" {
		t.Fatalf("err = %v want WORKFLOW_TRANSITION_INACTIVE", err)
	}
}

func TestRegisterTransitionToolRequiresDeps(t *testing.T) {
	if err := workflowphases.RegisterTransitionTool(nil, &workflowphases.Service{}); err == nil {
		t.Fatal("expected error for nil registry")
	}
	if err := workflowphases.RegisterTransitionTool(tools.NewDefaultRegistry(), nil); err == nil {
		t.Fatal("expected error for nil run manager")
	}
}
