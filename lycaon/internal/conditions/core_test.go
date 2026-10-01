package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoreWorkflowRunPredicates(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	run := &api.WorkflowRun{Status: api.WorkflowRunStatusRunning, CurrentPhase: "research"}
	vars := map[string]any{}
	ec := conditions.EvalContextFromRun(context.Background(), nil, run, vars)
	ok, err := reg.Evaluate("workflow_active", ec)
	if err != nil || !ok {
		t.Fatalf("workflow_active = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("phase_is:research", ec)
	if err != nil || !ok {
		t.Fatalf("phase_is:research = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("phase_is:approve", ec)
	if err != nil || !ok == false {
		t.Fatalf("phase_is:approve = %v err=%v want false", ok, err)
	}
	ok, err = reg.Evaluate("choice_transition_required", ec)
	if err != nil || ok {
		t.Fatalf("choice_transition_required = %v err=%v want false until the phase transition leaves", ok, err)
	}
}

func TestCoreUserFeedbackPredicates(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	vars := map[string]any{
		"user_feedback": map[string]any{
			"clarify": map[string]any{"prompt": "Which API?", "pending": true},
		},
	}
	ec := conditions.EvalContext{Vars: vars}
	ok, err := reg.Evaluate("user_feedback_pending:clarify", ec)
	if err != nil || !ok {
		t.Fatalf("pending = %v err=%v", ok, err)
	}
	vars = map[string]any{
		"user_feedback": map[string]any{
			"clarify": map[string]any{"prompt": "Which API?", "response": "GraphQL", "pending": false},
		},
	}
	ec.Vars = vars
	ok, err = reg.Evaluate("user_feedback_received:clarify", ec)
	if err != nil || !ok {
		t.Fatalf("received = %v err=%v", ok, err)
	}
}

func TestCoreUserDecisionPredicates(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	vars := map[string]any{
		"user_decision": map[string]any{
			"confirm": map[string]any{"prompt": "Proceed?", "options": []any{"yes", "no"}, "pending": true},
		},
	}
	ec := conditions.EvalContext{Vars: vars}
	ok, err := reg.Evaluate("user_decision_pending:confirm", ec)
	if err != nil || !ok {
		t.Fatalf("pending = %v err=%v", ok, err)
	}
	vars = map[string]any{
		"user_decision": map[string]any{
			"confirm": map[string]any{"choice": "yes", "pending": false},
		},
	}
	ec.Vars = vars
	ok, err = reg.Evaluate("user_decision_received:confirm", ec)
	if err != nil || !ok {
		t.Fatalf("received = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("user_decision:confirm,yes", ec)
	if err != nil || !ok {
		t.Fatalf("choice match = %v err=%v", ok, err)
	}
}

func TestTopologyStageCompleteBind(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	vars := map[string]any{
		"topology_stages": map[string]any{
			"research": map[string]any{"complete": true},
		},
	}
	ec := conditions.EvalContext{
		Vars:              vars,
		BindTopologyStage: "research",
	}
	ok, err := reg.Evaluate("topology_stage_complete", ec)
	if err != nil || !ok {
		t.Fatalf("topology complete = %v err=%v", ok, err)
	}
	ec.BindTopologyStage = "plan"
	ok, err = reg.Evaluate("topology_stage_complete", ec)
	if err != nil || ok {
		t.Fatalf("wrong stage = %v err=%v want false", ok, err)
	}
}

func TestParallelStagesCompleteBind(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	vars := map[string]any{
		"topology_stages": map[string]any{
			"review": map[string]any{"complete": true},
			"test":   map[string]any{"complete": true},
		},
	}
	ec := conditions.EvalContext{
		Vars:              vars,
		BindParallelGroup: []string{"review", "test"},
	}
	ok, err := reg.Evaluate("parallel_stages_complete", ec)
	if err != nil || !ok {
		t.Fatalf("parallel complete = %v err=%v", ok, err)
	}
}

func TestAgentIsParameterized(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{
		ToolName: "task",
		ToolArgs: map[string]any{"agent_type": "plan-writer"},
	}
	ok, err := reg.Evaluate("agent_is:plan-writer", ec)
	if err != nil || !ok {
		t.Fatalf("agent_is = %v err=%v", ok, err)
	}
}

func TestPostureIsParameterized(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{SessionPosture: api.SessionPostureSpec}
	ok, err := reg.Evaluate("posture_is:spec", ec)
	if err != nil || !ok {
		t.Fatalf("posture_is = %v err=%v", ok, err)
	}
}

func TestApprovalDeniedPredicate(t *testing.T) {
	denied := false
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		ApprovalDenied: func(_ context.Context, _ string) (bool, error) {
			return denied, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1"}
	ok, err := reg.Evaluate("approval_denied", ec)
	if err != nil || ok {
		t.Fatalf("initial approval_denied = %v err=%v", ok, err)
	}
	denied = true
	ok, err = reg.Evaluate("approval_denied", ec)
	if err != nil || !ok {
		t.Fatalf("after reject approval_denied = %v err=%v", ok, err)
	}
}
