package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func synthesisSiblingHistory() []api.Message {
	return []api.Message{
		{
			Role:    api.MessageRoleTool,
			Content: `<task agent_type="implementer" job_id="job-1" state="complete"><summary>done</summary></task>`,
		},
	}
}

func TestLoopSkipTurnRearmsWithoutKickOrPrompt(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	var kicks atomic.Int32
	history := synthesisSiblingHistory()
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	}
	deps.WorkerCycleIdle = func(_ context.Context, _ *api.Session, completingJobID string) (bool, error) {
		if strings.TrimSpace(completingJobID) == "job-1" {
			return false, nil // siblings still in flight
		}
		return true, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	deps.QueueInform = func(_ context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
		kicks.Add(1)
	}
	deps.HostWakeActionable = BuildHostWakeActionable(HostWakeActionableDeps{
		GetMessages: func(context.Context, string) ([]api.Message, error) {
			return history, nil
		},
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{WorkersInFlight: 1, PendingOverlayIDs: []string{}}
		},
	})
	engine.SetDeps(deps)
	engine.Waits.EnterSleep(context.Background(), "s1", time.Now().UTC().Add(30*time.Minute), "batch work", []WaitTrigger{
		WaitTriggerTimer,
		WaitTriggerNextWorkerDone,
	}, nil, SleepMoverHost)
	engine.Nudges.NudgeAfterWorkerJobTerminal(
		context.Background(),
		"s1",
		"job-1",
		anchor.WorkerTaskFinished,
		anchor.WorkerTaskFinished,
		"",
		anchor.Envelope{},
	)
	engine.Turns.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 on skip turn", prompts.Load())
	}
	if kicks.Load() != 0 {
		t.Fatalf("kicks = %d want 0 on skip turn", kicks.Load())
	}
	if !engine.Waits.IsSleeping("s1") {
		t.Fatal("expected sleep re-armed after skip turn")
	}
}

func TestLoopSkipTurnDoesNotApplyToDispatchWake(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	history := []api.Message{
		{
			Role:    api.MessageRoleTool,
			Content: `<task agent_type="` + orchestration.ProfileRepoResearcher + `" job_id="job-2" state="complete"><summary>paths cited</summary></task>`,
		},
	}
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	}
	deps.WorkerCycleIdle = func(_ context.Context, _ *api.Session, completingJobID string) (bool, error) {
		return strings.TrimSpace(completingJobID) == "job-2", nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	deps.HostWakeActionable = BuildHostWakeActionable(HostWakeActionableDeps{
		GetMessages: func(context.Context, string) ([]api.Message, error) {
			return history, nil
		},
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{PendingOverlayIDs: []string{}}
		},
	})
	engine.SetDeps(deps)
	engine.Nudges.NudgeAfterWorkerJobTerminal(
		context.Background(),
		"s1",
		"job-2",
		anchor.WorkerTaskFinished,
		anchor.WorkerTaskFinished,
		"",
		anchor.Envelope{},
	)
	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() >= 1 })
}
