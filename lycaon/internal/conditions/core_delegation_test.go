package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
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
	ec.ToolName = "delegate_dispatch"
	ok, err = reg.Evaluate("high_risk_tool", ec)
	if err != nil || !ok {
		t.Fatalf("high_risk_tool delegate = %v err=%v", ok, err)
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

func TestCoreDelegationPredicates(t *testing.T) {
	dep := &api.Delegation{
		ID:    "dep-1",
		Phase: api.DelegationPhaseWorker,
		Legs: []api.Leg{
			{ID: "leg-1", Status: api.LegStatusRunning, WorkerID: "job-1"},
			{ID: "leg-2", Status: api.LegStatusComplete, CompletionCriteria: []string{"tests"}},
		},
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore: conditions.StaticDelegationStore{Delegation: dep},
		GroundingBlocked: func(context.Context, string) (bool, error) {
			return true, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1"}

	cases := map[string]bool{
		"delegation_active":       true,
		"delegation_phase_worker": true,
		"delegation_phase_setup":  false,
		"leg_pending":             true,
		"all_legs_complete":       false,
		"worker_jobs_pending":     true,
		"grounding_blocked":       true,
		"completion_criteria_met": true,
	}
	for name, want := range cases {
		ok, err := reg.Evaluate(name, ec)
		if err != nil {
			t.Fatalf("%s err=%v", name, err)
		}
		if ok != want {
			t.Fatalf("%s = %v want %v", name, ok, want)
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
