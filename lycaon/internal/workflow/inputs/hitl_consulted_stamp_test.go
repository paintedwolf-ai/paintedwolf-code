//go:build integration

package inputs_test

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestAskUserTextResolveStampsHitlConsultedCurrentPhase(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := fx.ctx
	run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt": "Scope?",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
			Agent: orchestration.ProfileCoordinator},
	})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)

	_, err = fx.wfMgr.Feedback.ResolveUserFeedback(ctx, fx.sess.ID, run.ID, phaseID, "medium feature")
	testutil.FailErr(t, "ResolveUserFeedback", err)

	vars, err := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if stamped, _ := vars["hitl_consulted:work"].(bool); !stamped {
		t.Fatalf("expected hitl_consulted:work after text ask resolve; vars=%v", vars)
	}
}

func TestAskUserChoiceResolveStampsHitlConsultedCurrentPhase(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := fx.ctx
	run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt":        "Pick size",
		"response_type": "single_choice",
		"options":       []any{"small", "medium"},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
			Agent: orchestration.ProfileCoordinator},
	})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)

	_, err = fx.wfMgr.Feedback.ResolveUserDecision(ctx, fx.sess.ID, run.ID, phaseID, []string{"medium"}, "")
	testutil.FailErr(t, "ResolveUserDecision", err)

	vars, err := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if stamped, _ := vars["hitl_consulted:work"].(bool); !stamped {
		t.Fatalf("expected hitl_consulted:work after choice ask resolve; vars=%v", vars)
	}
}
