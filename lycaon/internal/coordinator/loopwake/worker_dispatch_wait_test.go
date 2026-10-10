package loopwake

import (
	"context"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"sync/atomic"
	"testing"
	"time"
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
			finish := f.loop.Admission.BeginPromptExecution(t.Context(), "session")
			defer finish()
			observe := f.loop.Observations.ObservePrompt("session")
			complete := func() {
				f.loop.Nudges.NudgeAfterWorkerJobTerminal(t.Context(), "session", "research", anchor.WorkerTaskFinished, "", "", anchor.Envelope{})
				f.loop.Cycles.OnWorkerCycleTerminal(t.Context(), "session", "research")
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
			f.loop.Waits.EnterSleep(t.Context(), "session", deadline, "research", []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}, nil, SleepMoverHost)
			f.loop.Waits.MarkWaitCalled("session")
			if boundary == "after sleep" {
				complete()
			}
			if f.prompts.Load() != 0 || resumes.Load() != 0 {
				t.Fatal("worker completion started overlapping coordinator execution")
			}
			if f.loop.Waits.OnTurnComplete(t.Context(), "session", false) != UserTurnContinues {
				t.Fatal("worker wait settled the visible turn")
			}
			f.drain(t, finish)
			if resumes.Load() != 1 || f.prompts.Load() != 0 || f.loop.Waits.IsSleeping("session") {
				t.Fatalf("resumes=%d prompts=%d sleeping=%v", resumes.Load(), f.prompts.Load(), f.loop.Waits.IsSleeping("session"))
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
	finish := f.loop.Admission.BeginPromptExecution(t.Context(), "session")
	defer finish()
	first := f.loop.Observations.ObservePrompt("session")
	f.loop.Nudges.NudgeAfterWorkerJobTerminal(t.Context(), "session", "comparison", anchor.WorkerTaskFinished, "", "", anchor.Envelope{})
	first(inject.CoordinatorTurnFrame{})
	// The next response includes the completed worker before another dispatch.
	f.loop.Observations.ObservePrompt("session")(inject.CoordinatorTurnFrame{})
	f.drain(t, finish)
	if f.prompts.Load() != 0 || f.loop.Nudges.HasPendingLoopWakes("session") {
		t.Fatalf("observed completion replayed: prompts=%d pending=%v", f.prompts.Load(), f.loop.Nudges.HasPendingLoopWakes("session"))
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
	f.loop.Turns.promptActive.Store("session", struct{}{})
	f.loop.Cycles.OnWorkerCycleTerminal(t.Context(), "session", "research")
	f.loop.Turns.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	if resumes.Load() != 0 {
		t.Fatal("wait delivery overlapped the host lane")
	}
	f.loop.Turns.releasePromptActiveAndRedrain(t.Context(), "session")
	f.loop.Turns.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	if resumes.Load() != 1 || f.loop.Nudges.HasPendingLoopWakes("session") {
		t.Fatalf("resumes=%d pending=%v", resumes.Load(), f.loop.Nudges.HasPendingLoopWakes("session"))
	}
}
