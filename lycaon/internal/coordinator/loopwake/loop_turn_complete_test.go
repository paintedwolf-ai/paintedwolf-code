package loopwake_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOnTurnCompleteHostProseOnlyDoesNotAutoPark(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-1"}, nil
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return false, nil
		},
	})
	continuation := loop.OnTurnComplete(context.Background(), "sess-1", true)
	if continuation != loopwake.UserTurnContinues {
		t.Fatalf("continuation = %v want host continuation while workers remain", continuation)
	}
	if loop.IsSleeping("sess-1") {
		t.Fatal("host prose-only turns with workers in flight must not auto-park")
	}
}

func TestOnTurnCompleteHostIdleParksWithoutTimer(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-idle"}, nil
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
	})
	continuation := loop.OnTurnComplete(context.Background(), "sess-idle", true)
	if continuation != loopwake.UserTurnSettled {
		t.Fatalf("continuation = %v want settled at an idle host boundary", continuation)
	}
	if !loop.IsSleeping("sess-idle") {
		t.Fatal("expected park after idle host turn without wait()/task()")
	}
	triggers := loop.WaitSubscriptionForTest("sess-idle")
	if containsWaitTrigger(triggers, loopwake.WaitTriggerTimer) {
		t.Fatalf("idle host park triggers = %v must omit timer", triggers)
	}
	if len(triggers) != 0 {
		t.Fatalf("idle host park triggers = %v want empty (user InterruptSleep only)", triggers)
	}
}

func TestOnTurnCompleteUserProseOnlyDoesNotPark(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	continuation := loop.OnTurnComplete(context.Background(), "sess-chat", false)
	if continuation != loopwake.UserTurnSettled {
		t.Fatalf("continuation = %v want prose-only user turn settled", continuation)
	}
	if loop.IsSleeping("sess-chat") {
		t.Fatal("user prose-only turns must not auto-park")
	}
}

func TestOnTurnCompleteParksForHumanApproval(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-hitl"}, nil
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		WorkflowSource: workflowFixturePorts(humanApprovalParkWF{
			run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "decide"},
		}),
	})
	continuation := loop.OnTurnComplete(context.Background(), "sess-hitl", false)
	if continuation != loopwake.UserTurnContinues {
		t.Fatalf("continuation = %v want checkpoint to remain in the current turn", continuation)
	}
	if !loop.IsSleeping("sess-hitl") {
		t.Fatal("expected park while human approval is awaiting")
	}
	if got := loop.SleepReasonForTest("sess-hitl"); got != "awaiting human approval" {
		t.Fatalf("reason = %q", got)
	}
	triggers := loop.WaitSubscriptionForTest("sess-hitl")
	if containsWaitTrigger(triggers, loopwake.WaitTriggerTimer) {
		t.Fatalf("human-approval park triggers = %v must omit timer", triggers)
	}
}

type humanApprovalParkWF struct {
	run *api.WorkflowRun
}

func (s humanApprovalParkWF) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return s.run, nil
}

func (s humanApprovalParkWF) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return map[string]any{"human_approval": map[string]any{"active": true, "ready": true}}, nil
}

func (s humanApprovalParkWF) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return true, nil
}

func (humanApprovalParkWF) HostObligationHeld(context.Context, string) (bool, error) {
	return false, nil
}

func (humanApprovalParkWF) HostObligationHoldKinds(context.Context, string) []string { return nil }

func TestOnTurnCompleteParksForHostObligation(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-ob"}, nil
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		// Host holds outrank workflow retry timers.
		WorkflowObligationsOpen: func(context.Context, string) bool { return true },
		WorkflowSource: workflowFixturePorts(hostObligationParkWF{
			run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "ingest"},
		}),
	})
	continuation := loop.OnTurnComplete(context.Background(), "sess-ob", true)
	if continuation != loopwake.UserTurnContinues {
		t.Fatalf("continuation = %v want host obligation to continue the turn", continuation)
	}
	if !loop.IsSleeping("sess-ob") {
		t.Fatal("expected park while a host obligation holds the phase")
	}
	if got := loop.SleepReasonForTest("sess-ob"); got != "awaiting host obligation: scan" {
		t.Fatalf("reason = %q", got)
	}
	triggers := loop.WaitSubscriptionForTest("sess-ob")
	if containsWaitTrigger(triggers, loopwake.WaitTriggerTimer) {
		t.Fatalf("host-obligation park triggers = %v must omit timer", triggers)
	}
}

func TestLoopWakeDeniedWhileHostObligationHeld(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-ob"}, nil
		},
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits: func(context.Context, *api.Session) settings.SessionLimits {
			return settings.DefaultSessionLimits()
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		WorkflowSource: workflowFixturePorts(hostObligationParkWF{
			run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "ingest"},
		}),
	})
	allow, reason, err := loop.Coordinator.Runtime.CoordinatorLoop().ShouldLoopWake(context.Background(), "sess-ob", anchor.PhaseAdvanced)
	if err != nil {
		t.Fatalf("ShouldLoopWake: %v", err)
	}
	if allow {
		t.Fatal("a held phase must not allow a spontaneous loop wake")
	}
	if reason != "host_obligation_held" {
		t.Fatalf("reason = %q want host_obligation_held", reason)
	}
}

type hostObligationParkWF struct {
	run *api.WorkflowRun
}

func (s hostObligationParkWF) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return s.run, nil
}

func (hostObligationParkWF) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (hostObligationParkWF) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return false, nil
}

func (hostObligationParkWF) HostObligationHeld(context.Context, string) (bool, error) {
	return true, nil
}

func (hostObligationParkWF) HostObligationHoldKinds(context.Context, string) []string {
	return []string{"scan"}
}

func TestOnTurnCompleteSkipsWhenWaitCalled(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	until := time.Now().UTC().Add(5 * time.Minute)
	loop.EnterSleep(context.Background(), "sess-3", until, "worker in flight", loopwake.DefaultCoordinatorWaitTriggers(false), nil, loopwake.SleepMoverHost)
	loop.MarkWaitCalled("sess-3")
	continuation := loop.OnTurnComplete(context.Background(), "sess-3", true)
	if continuation != loopwake.UserTurnContinues {
		t.Fatalf("continuation = %v want wait subscription to continue the turn", continuation)
	}
	st := loop.SleepUntilForTest("sess-3")
	if st.IsZero() || !st.Equal(until) {
		t.Fatalf("wait() sleep should not be overwritten by OnTurnComplete, got %v want %v", st, until)
	}
}

func containsWaitTrigger(triggers []loopwake.WaitTrigger, want loopwake.WaitTrigger) bool {
	for _, t := range triggers {
		if t == want {
			return true
		}
	}
	return false
}

func TestOnTurnCompleteObligationsOpenArmsWorkflowRetry(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-oblig"}, nil
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		WorkflowObligationsOpen: func(context.Context, string) bool { return true },
	})
	continuation := loop.OnTurnComplete(context.Background(), "sess-oblig", true)
	if continuation != loopwake.UserTurnContinues {
		t.Fatalf("continuation = %v want workflow obligation to continue the turn", continuation)
	}
	if !loop.IsSleeping("sess-oblig") {
		t.Fatal("expected workflow-obligation park after idle host turn")
	}
	if got := loop.SleepReasonForTest("sess-oblig"); got != "workflow obligations open" {
		t.Fatalf("sleep reason = %q want workflow obligations open", got)
	}
	triggers := loop.WaitSubscriptionForTest("sess-oblig")
	if !containsWaitTrigger(triggers, loopwake.WaitTriggerTimer) {
		t.Fatalf("obligation park triggers = %v must include timer", triggers)
	}
	if d := time.Until(loop.SleepUntilForTest("sess-oblig")); d <= 0 || d > 3*time.Minute {
		t.Fatalf("obligation park deadline %v want a short retry interval", d)
	}
}

func TestOnTurnCompleteObligationsOpenAppliesToUserTurns(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-oblig-user"}, nil
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		WorkflowObligationsOpen: func(context.Context, string) bool { return true },
	})
	loop.OnTurnComplete(context.Background(), "sess-oblig-user", false)
	if !loop.IsSleeping("sess-oblig-user") {
		t.Fatal("expected workflow-obligation park after idle user turn")
	}
	if got := loop.SleepReasonForTest("sess-oblig-user"); got != "workflow obligations open" {
		t.Fatalf("sleep reason = %q want workflow obligations open", got)
	}
}

func TestOnTurnCompleteObligationsSettledParksForUser(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-settled"}, nil
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		WorkflowObligationsOpen: func(context.Context, string) bool { return false },
	})
	loop.OnTurnComplete(context.Background(), "sess-settled", true)
	if got := loop.SleepReasonForTest("sess-settled"); got != "awaiting user after idle host turn" {
		t.Fatalf("sleep reason = %q want awaiting user after idle host turn", got)
	}
	if containsWaitTrigger(loop.WaitSubscriptionForTest("sess-settled"), loopwake.WaitTriggerTimer) {
		t.Fatal("settled-obligation idle park must omit timer")
	}
}

func TestLoopWakeBusyUsesPromptExecutionNotVisibleTurnStatus(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			// Visible turn status does not occupy the prompt execution lane.
			return &api.Session{ID: "sess-live", Status: api.SessionStatusBusy}, nil
		},
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits: func(context.Context, *api.Session) settings.SessionLimits {
			return settings.DefaultSessionLimits()
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		WorkflowSource: workflowFixturePorts(activeLoopWorkflow{}),
	})

	allow, busy := loop.EvaluateForTest(context.Background(), "sess-live", anchor.PhaseAdvanced)
	if !allow || busy {
		t.Fatalf("between executions = allow %v busy %v, want allow true busy false", allow, busy)
	}

	finish := loop.BeginPromptExecution(t.Context(), "sess-live")
	allow, busy = loop.EvaluateForTest(context.Background(), "sess-live", anchor.PhaseAdvanced)
	if !allow || !busy {
		t.Fatalf("during execution = allow %v busy %v, want allow true busy true", allow, busy)
	}
	finish()
	if loop.PromptExecutionActive("sess-live") {
		t.Fatal("prompt execution remained active after its runner finished")
	}
}

func TestUserTurnSettlementClaimOrdersLaterWakeAfterBoundary(t *testing.T) {
	loop := loopwake.NewLoopEngine()
	prompts := 0
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess-settling", Status: api.SessionStatusBusy}, nil
		},
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits: func(context.Context, *api.Session) settings.SessionLimits {
			return settings.DefaultSessionLimits()
		},
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		WorkflowSource:     workflowFixturePorts(activeLoopWorkflow{}),
		HostWakeActionable: func(context.Context, loopwake.HostWakeActionableInput) bool { return true },
		RunPrompt: func(context.Context, string) (*promptresult.Result, error) {
			prompts++
			return &promptresult.Result{}, nil
		},
	})

	release, claimed := loop.BeginUserTurnSettlement(t.Context(), "sess-settling")
	if !claimed {
		t.Fatal("settlement lane was not claimed")
	}
	loop.Nudge(t.Context(), "sess-settling", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	if _, ok := loop.PendingForTest("sess-settling"); !ok {
		t.Fatal("wake arriving after settlement claim was not ordered for redrain")
	}
	release()
	loop.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	if prompts != 1 {
		t.Fatalf("redrained prompts = %d want 1", prompts)
	}
}

type activeLoopWorkflow struct{}

func (activeLoopWorkflow) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return &api.WorkflowRun{ID: "run-live", Status: api.WorkflowRunStatusRunning}, nil
}

func (activeLoopWorkflow) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (activeLoopWorkflow) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return false, nil
}

func (activeLoopWorkflow) HostObligationHeld(context.Context, string) (bool, error) {
	return false, nil
}

func (activeLoopWorkflow) HostObligationHoldKinds(context.Context, string) []string {
	return nil
}
