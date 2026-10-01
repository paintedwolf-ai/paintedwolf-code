package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerCompletionAcrossWaitRegistrationResumesOnce(t *testing.T) {
	for _, boundary := range []string{"before lease", "before sleep", "after sleep"} {
		t.Run(boundary, func(t *testing.T) {
			f := newQueuedWaitFixture(t)
			f.deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
			var resumes atomic.Int32
			f.deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
				if delivery.Condition.Kind != "next_worker_done" || delivery.Condition.Outcome != "satisfied" {
					t.Errorf("wrong completion delivery: %+v", delivery)
				}
				resumes.Add(1)
				return &promptresult.Result{}, delivery.Admitted()
			}
			f.loop.SetDeps(f.deps)
			finish := f.loop.BeginPromptExecution(t.Context(), "session")
			defer finish()
			observe := f.loop.ObservePrompt("session")
			complete := func() {
				f.loop.NudgeAfterWorkerJobTerminal(t.Context(), "session", "research", anchor.WorkerTaskFinished, "", "", anchor.Envelope{})
				f.loop.OnWorkerCycleTerminal(t.Context(), "session", "research")
			}
			if boundary == "before lease" {
				complete()
			}
			observe(inject.CoordinatorTurnFrame{})
			deadline := time.Now().Add(time.Hour)
			lease, err := f.store.Arm(t.Context(), awaitstore.Lease{
				SessionID: "session", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "wait-research", ProfileID: "coordinator", Deadline: deadline,
				Conditions: []awaitstore.Condition{{Kind: "next_worker_done"}},
			})
			testutil.FailErr(t, "arm worker wait", err)
			if boundary == "before sleep" {
				complete()
			}
			f.loop.EnterSleep(t.Context(), "session", deadline, "research", []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}, nil, SleepMoverHost)
			f.loop.MarkWaitCalled("session")
			if boundary == "after sleep" {
				complete()
			}
			if f.prompts.Load() != 0 || resumes.Load() != 0 {
				t.Fatal("worker completion started overlapping coordinator execution")
			}
			if f.loop.OnTurnComplete(t.Context(), "session", false) != UserTurnContinues {
				t.Fatal("worker wait settled the visible turn")
			}
			f.drain(t, finish)
			if resumes.Load() != 1 || f.prompts.Load() != 0 || f.loop.IsSleeping("session") {
				t.Fatalf("resumes=%d prompts=%d sleeping=%v", resumes.Load(), f.prompts.Load(), f.loop.IsSleeping("session"))
			}
			_, active, err := f.store.ForSession(t.Context(), lease.SessionID)
			testutil.FailErr(t, "read settled worker wait", err)
			if active {
				t.Fatal("completion left the durable wait armed")
			}
		})
	}
}

func TestWorkerCompletionObservedDuringDispatchDoesNotStartExtraTurn(t *testing.T) {
	f := newQueuedWaitFixture(t)
	f.deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
	f.loop.SetDeps(f.deps)
	finish := f.loop.BeginPromptExecution(t.Context(), "session")
	defer finish()
	first := f.loop.ObservePrompt("session")
	f.loop.NudgeAfterWorkerJobTerminal(t.Context(), "session", "comparison", anchor.WorkerTaskFinished, "", "", anchor.Envelope{})
	first(inject.CoordinatorTurnFrame{})
	// The next response includes the completed worker before another dispatch.
	f.loop.ObservePrompt("session")(inject.CoordinatorTurnFrame{})
	f.drain(t, finish)
	if f.prompts.Load() != 0 || f.loop.HasPendingLoopWakes("session") {
		t.Fatalf("observed completion replayed: prompts=%d pending=%v", f.prompts.Load(), f.loop.HasPendingLoopWakes("session"))
	}
}

func TestWorkerWaitDeliveryFollowsHostLaneRelease(t *testing.T) {
	f := newQueuedWaitFixture(t)
	f.deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
	var resumes atomic.Int32
	f.deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		resumes.Add(1)
		return &promptresult.Result{}, delivery.Admitted()
	}
	f.loop.SetDeps(f.deps)
	_, err := f.store.Arm(t.Context(), awaitstore.Lease{
		SessionID: "session", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "wait-research", ProfileID: "coordinator",
		Deadline: time.Now().Add(time.Hour), Conditions: []awaitstore.Condition{{Kind: "next_worker_done"}},
	})
	testutil.FailErr(t, "arm worker wait", err)
	f.loop.promptActive.Store("session", struct{}{})
	f.loop.OnWorkerCycleTerminal(t.Context(), "session", "research")
	f.loop.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	if resumes.Load() != 0 {
		t.Fatal("wait delivery overlapped the host lane")
	}
	f.loop.releasePromptActiveAndRedrain(t.Context(), "session")
	f.loop.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	if resumes.Load() != 1 || f.loop.HasPendingLoopWakes("session") {
		t.Fatalf("resumes=%d pending=%v", resumes.Load(), f.loop.HasPendingLoopWakes("session"))
	}
}
