package loopwake

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func activityLoop(actionable bool, runPrompt func(context.Context, string) (*promptresult.Result, error)) *LoopEngine {
	loop := NewLoopEngine()
	loop.SetDeps(LoopDeps{
		GetSession: func(_ context.Context, id string) (*api.Session, error) {
			return &api.Session{ID: id, Status: api.SessionStatusIdle}, nil
		},
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		WorkflowSource:       workflowFixturePorts(activeLoopWorkflow{}),
		HostWakeActionable:   func(context.Context, HostWakeActionableInput) bool { return actionable },
		RunPrompt:            runPrompt,
	})
	return loop
}

func lastDecision(t *testing.T, activity SessionActivity) WakeDecision {
	t.Helper()
	if len(activity.Decisions) == 0 {
		t.Fatal("no wake decision recorded")
	}
	return activity.Decisions[len(activity.Decisions)-1]
}

// A skipped wake leaves nothing that could open a turn: the session reads idle
// and the ledger says why the wake went nowhere.
func TestParkedWakeLeavesTheSessionIdleWithItsReason(t *testing.T) {
	loop := activityLoop(false, func(context.Context, string) (*promptresult.Result, error) {
		t.Fatal("a skipped wake opened a turn")
		return nil, nil
	})
	before := loop.Activity(t.Context(), "sess")
	loop.Nudges.Nudge(t.Context(), "sess", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	loop.Turns.WaitForAsyncTurns(t.Context())

	after := loop.Activity(t.Context(), "sess")
	if len(after.Busy) != 0 {
		t.Fatalf("busy = %v after a skipped wake, want none", after.Busy)
	}
	if after.Epoch == before.Epoch {
		t.Fatal("deciding a wake did not change the activity epoch")
	}
	if got := lastDecision(t, after); got.Wake != anchor.PhaseAdvanced || got.Outcome != "skip:non_actionable" {
		t.Fatalf("decision = %+v, want PhaseAdvanced skip:non_actionable", got)
	}
}

// A wake on its way to a turn keeps the session busy until the turn ends.
func TestDeliveredWakeKeepsTheSessionBusyUntilItsTurnEnds(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	loop := activityLoop(true, func(context.Context, string) (*promptresult.Result, error) {
		close(entered)
		<-release
		return &promptresult.Result{}, nil
	})
	loop.Nudges.Nudge(t.Context(), "sess", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	testutil.AwaitClosed(t, "the wake opens a turn", entered)

	running := loop.Activity(t.Context(), "sess")
	if !slices.Contains(running.Busy, ActivityHostTurn) {
		t.Fatalf("busy = %v while the turn runs, want %s", running.Busy, ActivityHostTurn)
	}
	close(release)
	loop.Turns.WaitForAsyncTurns(t.Context())

	settled := loop.Activity(t.Context(), "sess")
	if len(settled.Busy) != 0 {
		t.Fatalf("busy = %v after the turn, want none", settled.Busy)
	}
	outcomes := make([]string, 0, len(settled.Decisions))
	for _, d := range settled.Decisions {
		outcomes = append(outcomes, d.Outcome)
	}
	if !slices.Equal(outcomes, []string{"queued_ready", "run_prompt"}) {
		t.Fatalf("decisions = %v, want queued_ready then run_prompt", outcomes)
	}
}

// Marked work outside the loop, such as a submitted prompt, keeps the session
// busy for exactly as long as it is held.
func TestMarkedWorkHoldsTheSessionBusy(t *testing.T) {
	loop := activityLoop(true, nil)
	end := loop.Nudges.BeginPromptSubmission("sess")
	if busy := loop.Activity(t.Context(), "sess").Busy; !slices.Equal(busy, []ActivityKind{ActivityPromptSubmission}) {
		t.Fatalf("busy = %v while a prompt is submitted", busy)
	}
	if other := loop.Activity(t.Context(), "other").Busy; len(other) != 0 {
		t.Fatalf("another session reads busy: %v", other)
	}
	end()
	end()
	if busy := loop.Activity(t.Context(), "sess").Busy; len(busy) != 0 {
		t.Fatalf("busy = %v after the prompt returned", busy)
	}
}
