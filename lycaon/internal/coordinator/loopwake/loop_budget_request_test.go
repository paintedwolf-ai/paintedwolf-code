package loopwake

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// drainAsyncTurns waits for host turns before temporary resources close.
func drainAsyncTurns(t *testing.T, engine *LoopEngine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	engine.WaitForAsyncTurns(ctx)
}

func TestBudgetRequestRunsWhileWorkerInFlight(t *testing.T) {
	engine := NewLoopEngine()
	defer drainAsyncTurns(t, engine)
	ws := t.TempDir()
	var prompts atomic.Int32
	var requestKicks atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: ws}, nil
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
	deps.QueueInform = func(_ context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
		if inform == anchor.WorkerBudgetRequested {
			requestKicks.Add(1)
		}
	}
	engine.SetDeps(deps)

	engine.NudgeWorkerBudgetRequested(context.Background(), "s1", "job-1", budgetRequestEnvelope("job-1"))
	drainAsyncTurns(t, engine)
	if requestKicks.Load() != 1 {
		t.Fatalf("budget-request kicks = %d want 1 while worker running", requestKicks.Load())
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d want 1 while worker running", prompts.Load())
	}
}

func TestBudgetRequestKickCarriesRequestWhileBusy(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	kicks := &kick.KickEngine{}
	kicks.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: moduleRoot}))

	engine := NewLoopEngine()
	defer drainAsyncTurns(t, engine)
	ws := t.TempDir()
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: ws}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) {
		return false, nil
	}
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		return &promptresult.Result{}, nil
	}
	deps.QueueInform = func(_ context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
		kicks.QueueDeferred(sessionID, anchor.InformRender(inform), env.KickOptions()...)
	}
	engine.SetDeps(deps)

	engine.NudgeWorkerBudgetRequested(context.Background(), "s1", "job-A", budgetRequestEnvelope("job-A"))
	drainAsyncTurns(t, engine)

	wantID := anchor.InformRender(anchor.WorkerBudgetRequested)
	if id := kicks.TakePendingKickID("s1"); id != wantID {
		t.Fatalf("pending kick id = %q want %q", id, wantID)
	}
	text, _, ok, _ := kicks.RenderPendingNudge(t.Context(), "s1", kick.CoordinatorKickRenderContext{})
	if !ok {
		t.Fatal("budget request rendered empty")
	}
	for _, want := range []string{`extend_worker_budget(job_id="job-A", max_tool_loops=32)`, "trace the remaining callers"} {
		if !strings.Contains(text, want) {
			t.Fatalf("budget request = %q want %q", text, want)
		}
	}
}

func TestBudgetRequestDeferralIsJobScoped(t *testing.T) {
	engine := NewLoopEngine()
	q := engine.sessionDeferredQueue("s1")
	q.push(pendingLoopWake{wake: anchor.WorkerBudgetRequested, inform: anchor.WorkerBudgetRequested, legID: "job-1"})
	q.push(pendingLoopWake{wake: anchor.WorkerBudgetRequested, inform: anchor.WorkerBudgetRequested, legID: "job-2"})
	q.push(pendingLoopWake{wake: anchor.LegFinished, inform: anchor.LegFinished, legID: "leg-1"})

	engine.dropDeferredBudgetRequestForJob("s1", "job-1")

	remaining := q.drain()
	if len(remaining) != 2 {
		t.Fatalf("remaining = %d want 2 (job-2 request + leg-finished kept)", len(remaining))
	}
	for _, d := range remaining {
		if d.wake == anchor.WorkerBudgetRequested && d.legID == "job-1" {
			t.Fatal("job-1 budget request should have been evicted")
		}
	}
}

func TestDrainPendingSkipsNonActionable(t *testing.T) {
	engine := NewLoopEngine()
	defer drainAsyncTurns(t, engine)
	ws := t.TempDir()
	var prompts atomic.Int32
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle, WorkspacePath: ws}, nil
	}
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
	})
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)

	engine.enqueuePending("s1", pendingLoopWake{wake: anchor.WaitTimerFired, seq: engine.nudgeSeq.Add(1)})
	engine.DrainPending(context.Background(), "s1")

	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 — non-actionable drained nudge bypassed the skip gate", prompts.Load())
	}
	if _, ok := engine.PendingForTest("s1"); ok {
		t.Fatal("skipped pending nudge should be consumed, not left queued")
	}
}
