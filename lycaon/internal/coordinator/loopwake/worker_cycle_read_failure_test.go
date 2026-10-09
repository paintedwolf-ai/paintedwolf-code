package loopwake

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/promptresult"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerCycleReadFailureDoesNotStallTheLoop(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	// (false, err) mirrors a failed queue read: the false is a zero value.
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) {
		return false, errors.New("worker queue read failed")
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	engine.Nudge(context.Background(), "s1", anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})

	// Exercise terminal and drain release paths.
	engine.OnWorkerCycleTerminal(context.Background(), "s1", "")
	engine.DrainPending(context.Background(), "s1")

	testutil.WaitFor(t, 2*time.Second, func() bool { return prompts.Load() >= 1 })
}

func TestWorkerCycleGenuinelyBusyStillDefers(t *testing.T) {
	workspaceDir := t.TempDir()
	engine := NewLoopEngine()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: workspaceDir}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) {
		return false, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	engine.Nudge(context.Background(), "s1", anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	engine.DrainPending(context.Background(), "s1")
	time.Sleep(100 * time.Millisecond)

	if got := prompts.Load(); got != 0 {
		t.Fatalf("prompts = %d want 0 while workers are genuinely in flight", got)
	}
}
