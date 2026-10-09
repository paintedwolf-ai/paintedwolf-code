package coordinator_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoopEvaluateDeniesWhenDisabled(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	disabled := settings.DefaultSessionLimits()
	f := false
	disabled.CoordinatorLoop = &f
	engine.SetDeps(loopwake.LoopDeps{
		GetSession: func(_ context.Context, _ string) (*api.Session, error) {
			return &api.Session{Status: api.SessionStatusIdle}, nil
		},
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return disabled },
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
	})
	allow, busy := engine.EvaluateForTest(context.Background(), "s1", anchor.LegFinished)
	if allow || busy {
		t.Fatalf("allow=%v busy=%v", allow, busy)
	}
}

func TestLoopBudgetConsumption(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	limits := settings.DefaultSessionLimits()
	limits.MaxCoordinatorLoopCycles = 1
	sess := &api.Session{Status: api.SessionStatusIdle}
	engine.SetDeps(loopwake.LoopDeps{
		GetSession: func(_ context.Context, _ string) (*api.Session, error) { return sess, nil },
		Limits:     func(context.Context, *api.Session) settings.SessionLimits { return limits },
		WorkflowSource: loopWorkflowDomains(stubLoopWF{
			run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "implement"},
		}),
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
	})
	ctx := context.Background()
	if !engine.TryConsumeBudgetForTest(ctx, "s1", "run-1") {
		t.Fatal("first consume should succeed")
	}
	if engine.TryConsumeBudgetForTest(ctx, "s1", "run-1") {
		t.Fatal("second consume should fail at max=1")
	}
}

type stubLoopWF struct {
	run  *api.WorkflowRun
	vars map[string]any
}

func (s stubLoopWF) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return s.run, nil
}

func (s stubLoopWF) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	if s.vars != nil {
		return s.vars, nil
	}
	return map[string]any{}, nil
}

func (s stubLoopWF) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return scaffoldvars.HumanApprovalAwaiting(s.vars), nil
}

func (stubLoopWF) HostObligationHeld(context.Context, string) (bool, error) { return false, nil }

func (stubLoopWF) HostObligationHoldKinds(context.Context, string) []string { return nil }

func TestLoopScheduleAndDrain(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	limits := settings.DefaultSessionLimits()
	sess := &api.Session{ID: "s1", Status: api.SessionStatusBusy}
	engine.SetDeps(loopwake.LoopDeps{
		GetSession: func(_ context.Context, id string) (*api.Session, error) {
			return sess, nil
		},
		Limits: func(context.Context, *api.Session) settings.SessionLimits { return limits },
		WorkflowSource: loopWorkflowDomains(stubLoopWF{
			run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "implement"},
		}),
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
	})
	ctx := context.Background()
	finishExecution := engine.BeginPromptExecution(t.Context(), "s1")
	engine.Nudge(ctx, "s1", anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{})
	if _, ok := engine.PendingForTest("s1"); !ok {
		t.Fatal("expected pending loop wake while prompt execution is active")
	}
	finishExecution()
	if !engine.TryConsumeBudgetForTest(ctx, "s1", "run-1") {
		t.Fatal("expected budget consume on drain path")
	}
	engine.DrainPending(ctx, "s1")
	if _, ok := engine.PendingForTest("s1"); ok {
		t.Fatal("pending should be cleared")
	}
}

func TestLoopShouldLoopWakeHumanApprovalAwaiting(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	deps := loopwake.LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{Status: api.SessionStatusIdle}, nil
	}
	deps.WorkflowSource = loopWorkflowDomains(stubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "decide"},
		vars: map[string]any{"human_approval": map[string]any{
			"active": true, "ready": true, "blueprint_path": "review.md",
		}},
	})
	engine.SetDeps(deps)
	allow, reason, err := engine.ShouldLoopWake(context.Background(), "s1", anchor.LegFinished)
	if err != nil || allow || reason != "human_approval_awaiting" {
		t.Fatalf("allow=%v reason=%q err=%v", allow, reason, err)
	}
}

func TestLoopShouldLoopWakeIgnoresApprovePhaseName(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	deps := loopwake.LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{Status: api.SessionStatusIdle}, nil
	}
	deps.WorkflowSource = loopWorkflowDomains(stubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "approve"},
	})
	engine.SetDeps(deps)
	allow, reason, err := engine.ShouldLoopWake(context.Background(), "s1", anchor.LegFinished)
	if err != nil {
		t.Fatalf("ShouldLoopWake: %v", err)
	}
	if reason == "approve_phase" || reason == "human_approval_awaiting" {
		t.Fatalf("phase name must not deny, reason=%q", reason)
	}
	if !allow {
		t.Fatalf("allow=%v reason=%q want allow without awaiting vars", allow, reason)
	}
}

func TestLoopScheduleLegFinished(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	kicked := false
	deps := loopwake.LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{Status: api.SessionStatusBusy}, nil
	}
	deps.WorkflowSource = loopWorkflowDomains(stubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "implement"},
	})
	var informed anchor.Envelope
	deps.QueueInform = func(_ context.Context, _ string, _ anchor.ID, env anchor.Envelope) {
		kicked = true
		informed = env
	}
	engine.SetDeps(deps)
	engine.NudgeLegFinished(context.Background(), "s1", time.Now(), "leg-1")
	if !kicked {
		t.Fatal("expected leg finished kick while session busy")
	}
	// The kick names the leg so a recall for omitted detail can address it.
	if got, _ := informed.Vars["leg_id"].(string); got != "leg-1" {
		t.Fatalf("leg finished envelope leg_id = %q, want leg-1", got)
	}
}

func loopWorkflowDomains(source stubLoopWF) *loopwake.WorkflowDomains {
	return &loopwake.WorkflowDomains{Runs: source, Approvals: source, Obligations: source}
}
