package conditions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoreToolPredicates(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{
		ToolName: "write",
		ToolArgs: map[string]any{},
	}
	ok, err := reg.Evaluate("tool_is_write", ec)
	if err != nil || !ok {
		t.Fatalf("tool_is_write = %v err=%v", ok, err)
	}
	ec.ToolName = "command"
	ok, err = reg.Evaluate("tool_is_command", ec)
	if err != nil || !ok {
		t.Fatalf("tool_is_command = %v err=%v", ok, err)
	}
	ec.ToolName = "task"
	ec.ToolArgs = map[string]any{"agent_type": "implementer"}
	ok, err = reg.Evaluate("high_risk_tool", ec)
	if err != nil || !ok {
		t.Fatalf("high_risk_tool implementer = %v err=%v", ok, err)
	}
	ec.ToolName = "instruct_worker"
	ok, err = reg.Evaluate("high_risk_tool", ec)
	if err != nil || !ok {
		t.Fatalf("high_risk_tool instruct_worker to implementer = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("tool_is_delegation", ec)
	if err != nil || !ok {
		t.Fatalf("tool_is_delegation instruct_worker = %v err=%v", ok, err)
	}
	ec.ToolArgs = map[string]any{"worker": "scout"}
	ok, err = reg.Evaluate("high_risk_tool", ec)
	if err != nil || ok {
		t.Fatalf("high_risk_tool instruct_worker without an agent = %v err=%v", ok, err)
	}
	ec.ToolName = "plan_from_blueprint"
	ok, err = reg.Evaluate("high_risk_tool", ec)
	if err != nil || !ok {
		t.Fatalf("high_risk_tool plan_from_blueprint = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("tool_is_delegation", ec)
	if err != nil || !ok {
		t.Fatalf("tool_is_delegation plan_from_blueprint = %v err=%v", ok, err)
	}
	ec.ToolName = "delegate_dispatch"
	ok, err = reg.Evaluate("tool_is_delegation", ec)
	if err != nil || ok {
		t.Fatalf("tool_is_delegation delegate_dispatch = %v err=%v want false", ok, err)
	}
	ok, err = reg.Evaluate("high_risk_tool", ec)
	if err != nil || ok {
		t.Fatalf("high_risk_tool delegate_dispatch = %v err=%v want false", ok, err)
	}
}

func TestCoreSpawnGuards(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DoomLoopExceeded: func(context.Context, string) (bool, error) { return true, nil },
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{
		Ctx:           context.Background(),
		SessionID:     "sess-1",
		ToolName:      "task",
		ToolArgs:      map[string]any{"agent_type": "blocked-agent"},
		AllowedAgents: []string{"writer"},
	}
	ok, err := reg.Evaluate("disallowed_agent", ec)
	if err != nil || !ok {
		t.Fatalf("disallowed_agent = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("doom_loop_exceeded", ec)
	if err != nil || !ok {
		t.Fatalf("doom_loop_exceeded = %v err=%v", ok, err)
	}
}

func TestCoreGroundingAndRetiredPredicates(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		GroundingBlocked: func(context.Context, string) (bool, error) {
			return true, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1"}

	ok, err := reg.Evaluate("grounding_blocked", ec)
	if err != nil || !ok {
		t.Fatalf("grounding_blocked = %v err=%v", ok, err)
	}

	retired := []string{
		"delegation_active",
		"delegation_phase_setup",
		"delegation_phase_worker",
		"delegation_phase_done",
		"leg_pending",
		"all_legs_complete",
		"leg_failed",
		"worker_jobs_pending",
		"completion_criteria_met",
		"file_modified_since_base",
	}
	for _, name := range retired {
		ok, err := reg.Evaluate(name, ec)
		if ok || !errors.Is(err, conditions.ErrUnknownCondition) {
			t.Fatalf("%s = (%v, %v) want (false, ErrUnknownCondition)", name, ok, err)
		}
	}
}

func TestCoreHostGuards(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		ApprovalDenied: func(context.Context, string) (bool, error) { return true, nil },
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", Vars: map[string]any{"iteration_cap_near": true}}
	for _, id := range []string{"approval_denied", "iteration_cap_near"} {
		ok, err := reg.Evaluate(id, ec)
		if err != nil || !ok {
			t.Fatalf("%s = %v err=%v", id, ok, err)
		}
	}
}

func TestCoreAgentIsPredicate(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{
		ToolName: "task",
		ToolArgs: map[string]any{"agent_type": "PlanWriter"},
	}
	ok, err := reg.Evaluate("agent_is:planwriter", ec)
	if err != nil || !ok {
		t.Fatalf("agent_is = %v err=%v", ok, err)
	}
}
