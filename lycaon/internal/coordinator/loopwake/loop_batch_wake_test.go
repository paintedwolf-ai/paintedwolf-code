package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func closedBatchLoopWF(seq int) StubLoopWF {
	return StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
		vars: map[string]any{
			"coordinator_batch": map[string]any{
				"phase": batch.PhaseClosed,
				"seq":   seq,
			},
		},
	}
}

func TestLoopSkipScheduledWhenBatchClosed(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	var kicks atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(closedBatchLoopWF(2))
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	deps.QueueInform = func(_ context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
		kicks.Add(1)
	}
	deps.DropPendingKicksForBatchSeq = func(string, int) {}
	engine.SetDeps(deps)
	engine.EnterSleep(context.Background(), "s1", time.Now().UTC().Add(30*time.Minute), "wait", []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverHost)
	engine.Nudge(context.Background(), "s1", anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	engine.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 when batch closed", prompts.Load())
	}
	if kicks.Load() != 0 {
		t.Fatalf("kicks = %d want 0 when batch closed", kicks.Load())
	}
	triggers := engine.WaitSubscriptionForTest("s1")
	for _, tr := range triggers {
		if tr == WaitTriggerTimer {
			t.Fatal("timer subscription should be disarmed when batch is closed")
		}
	}
}

func TestLoopDropScheduledWhenNonActionable(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
		vars: map[string]any{
			"coordinator_batch": map[string]any{
				"phase": batch.PhaseSynthesize,
				"seq":   1,
			},
		},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	engine.SetDeps(deps)
	engine.Nudge(context.Background(), "s1", anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	engine.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 for non-actionable scheduled wake", prompts.Load())
	}
}

func TestLoopDropPhaseAdvanceWhenIntentAlreadySettled(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	var kicks atomic.Int32
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "Tell me about this repo", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Kind:       api.MessageKindCompletionReport,
			Content:    "Repository overview",
			Visibility: api.MessageVisibilityTranscript,
		},
	}
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.HostWakeActionable = BuildHostWakeActionable(HostWakeActionableDeps{
		GetMessages: func(context.Context, string) ([]api.Message, error) { return history, nil },
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "s1"}, nil
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{}
		},
		ActiveRun: func(context.Context, string) (*api.WorkflowRun, error) {
			return &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning}, nil
		},
		// The entered worker-cycle phase is dormant until a worker event.
		WorkflowObligationsOpen: func(context.Context, string) bool { return false },
	})
	deps.QueueInform = func(context.Context, string, anchor.ID, anchor.Envelope) {
		kicks.Add(1)
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	engine.Nudge(context.Background(), "s1", anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	engine.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 after settled intent", prompts.Load())
	}
	if kicks.Load() != 0 {
		t.Fatalf("phase kicks = %d want 0 without a model turn", kicks.Load())
	}
}

func TestLoopPhaseAdvanceKeepsNewWorkflowWorkActionable(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "Run the review workflow", Visibility: api.MessageVisibilityTranscript},
		{
			Role:       api.MessageRoleAssistant,
			Kind:       api.MessageKindCompletionReport,
			Content:    "Interim report",
			Visibility: api.MessageVisibilityTranscript,
		},
	}
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "execute"},
	})
	deps.HostWakeActionable = BuildHostWakeActionable(HostWakeActionableDeps{
		GetMessages: func(context.Context, string) ([]api.Message, error) { return history, nil },
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "s1"}, nil
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{}
		},
		ActiveRun: func(context.Context, string) (*api.WorkflowRun, error) {
			return &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning}, nil
		},
		WorkflowObligationsOpen: func(context.Context, string) bool { return true },
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	engine.Nudge(context.Background(), "s1", anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() == 1 })
}

func TestLoopPhaseAdvanceStartsEnteredPhaseBeforeCloseout(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "Run the workflow", Visibility: api.MessageVisibilityTranscript},
	}
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "review"},
	})
	deps.HostWakeActionable = BuildHostWakeActionable(HostWakeActionableDeps{
		GetMessages: func(context.Context, string) ([]api.Message, error) { return history, nil },
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "s1"}, nil
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{}
		},
		ActiveRun: func(context.Context, string) (*api.WorkflowRun, error) {
			return &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning}, nil
		},
		WorkflowObligationsOpen: func(context.Context, string) bool { return false },
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	engine.Nudge(context.Background(), "s1", anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() == 1 })
}

func TestLoopDropStaleBatchSeqWake(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
		vars: map[string]any{
			"coordinator_batch": map[string]any{
				"phase": batch.PhaseDispatch,
				"seq":   3,
			},
		},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	deps.DropPendingKicksBeforeBatchSeq = func(string, int) {}
	engine.SetDeps(deps)
	engine.Nudge(
		context.Background(),
		"s1",
		anchor.WaitTimerFired,
		anchor.WaitTimerFired,
		"",
		anchor.Envelope{BatchSeq: 2, BatchSeqSet: true},
	)
	engine.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 for stale batch_seq wake", prompts.Load())
	}
}

func TestLoopScheduledDroppedDuringPromptExecution(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusBusy, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	finishExecution := engine.BeginPromptExecution(t.Context(), "s1")
	defer finishExecution()
	engine.Nudge(context.Background(), "s1", anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	engine.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	if prompts.Load() != 0 {
		t.Fatalf("scheduled wake during prompt execution should drop, prompts = %d", prompts.Load())
	}
	if nudge, ok := engine.sessionPendingQueue("s1").peek(); ok {
		t.Fatalf("scheduled wake should not enqueue pending, got %q", nudge.wake)
	}
}

func TestDisarmTimerBackstopRemovesTimerTrigger(t *testing.T) {
	engine := NewLoopEngine()
	engine.EnterSleep(context.Background(), "s1", time.Now().UTC().Add(time.Hour), "test", []WaitTrigger{
		WaitTriggerTimer,
		WaitTriggerAllWorkersIdle,
	}, nil, SleepMoverHost)
	engine.DisarmTimerBackstop(t.Context(), "s1")
	triggers := engine.WaitSubscriptionForTest("s1")
	for _, tr := range triggers {
		if tr == WaitTriggerTimer {
			t.Fatal("timer trigger should be removed after disarm")
		}
	}
	if len(triggers) != 1 || triggers[0] != WaitTriggerAllWorkersIdle {
		t.Fatalf("triggers = %v want [all_workers_idle]", triggers)
	}
}

func TestLoopScheduledRunsWhenObligationsOpenOnClosedBatch(t *testing.T) {
	workspaceDir := t.TempDir()
	// Open workflow obligations keep timer wakes actionable.
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(closedBatchLoopWF(2))
	deps.WorkflowObligationsOpen = func(context.Context, string) bool { return true }
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	engine.EnterSleep(context.Background(), "s1", time.Now().UTC().Add(30*time.Minute), "wait", []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverHost)
	engine.Nudge(context.Background(), "s1", anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	engine.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d want 1 for timer wake with open obligations", prompts.Load())
	}
}
