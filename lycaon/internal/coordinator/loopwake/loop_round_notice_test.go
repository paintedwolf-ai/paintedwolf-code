package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// drainAsyncTurns waits for host turns before temporary resources close.
func drainAsyncTurns(t *testing.T, engine *LoopEngine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	engine.Turns.WaitForAsyncTurns(ctx)
}

func TestRoundNoticeRunsWhileWorkerInFlight(t *testing.T) {
	engine := NewLoopEngine()
	defer drainAsyncTurns(t, engine)
	ws := t.TempDir()
	var prompts atomic.Int32
	var noticeKicks atomic.Int32
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
		if inform == anchor.WorkerRoundNotice {
			noticeKicks.Add(1)
		}
	}
	engine.SetDeps(deps)

	engine.Nudges.NudgeWorkerRoundNotice(context.Background(), "s1", "job-1", roundNoticeEnvelope("job-1"))
	drainAsyncTurns(t, engine)
	if noticeKicks.Load() != 1 {
		t.Fatalf("round notice kicks = %d want 1 while worker running", noticeKicks.Load())
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d want 1 while worker running", prompts.Load())
	}
}

func TestRoundNoticeKickCarriesMeasurementsWhileBusy(t *testing.T) {
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

	engine.Nudges.NudgeWorkerRoundNotice(context.Background(), "s1", "job-A", roundNoticeEnvelope("job-A"))
	drainAsyncTurns(t, engine)

	wantID := anchor.InformRender(anchor.WorkerRoundNotice)
	if id := kicks.TakePendingKickID("s1"); id != wantID {
		t.Fatalf("pending kick id = %q want %q", id, wantID)
	}
	text, _, ok, _ := kicks.RenderPendingNudge(t.Context(), "s1", kick.CoordinatorKickRenderContext{})
	if !ok {
		t.Fatal("round notice rendered empty")
	}
	facts := roundNoticeEnvelope("job-A").WorkerRoundNotice
	for _, want := range []string{facts.JobID, facts.AgentType, strconv.Itoa(facts.ModelCalls)} {
		if !strings.Contains(text, want) {
			t.Fatalf("round notice = %q want %q", text, want)
		}
	}
}

func TestRoundNoticeDeferralIsJobScoped(t *testing.T) {
	engine := NewLoopEngine()
	q := engine.Nudges.sessionDeferredQueue("s1")
	q.push(pendingLoopWake{wake: anchor.WorkerRoundNotice, inform: anchor.WorkerRoundNotice, legID: "job-1"})
	q.push(pendingLoopWake{wake: anchor.WorkerRoundNotice, inform: anchor.WorkerRoundNotice, legID: "job-2"})
	q.push(pendingLoopWake{wake: anchor.LegFinished, inform: anchor.LegFinished, legID: "leg-1"})

	engine.Nudges.dropDeferredRoundNoticesForJob("s1", "job-1")

	remaining := q.drain()
	if len(remaining) != 2 {
		t.Fatalf("remaining = %d want 2 (job-2 notice + leg-finished kept)", len(remaining))
	}
	for _, d := range remaining {
		if d.wake == anchor.WorkerRoundNotice && d.legID == "job-1" {
			t.Fatal("job-1 round notice should have been evicted")
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

	engine.Nudges.enqueuePending("s1", pendingLoopWake{wake: anchor.WaitTimerFired, seq: engine.Nudges.nudgeSeq.Add(1)})
	engine.Nudges.DrainPending(context.Background(), "s1")

	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 — non-actionable drained nudge bypassed the skip gate", prompts.Load())
	}
	if _, ok := engine.Nudges.Pending("s1"); ok {
		t.Fatal("skipped pending nudge should be consumed, not left queued")
	}
}
