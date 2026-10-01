package loopwake

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

// A worker's budget request needs its coordinator the way a decision does, so
// every wait on workers wakes for it and no other wait does.
func TestWorkerBudgetRequestWakesEveryWorkerWait(t *testing.T) {
	cases := []struct {
		name     string
		triggers []WaitTrigger
		want     WaitTrigger
	}{
		{"next worker", []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}, WaitTriggerNextWorkerDone},
		{"all workers", []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}, WaitTriggerAllWorkersIdle},
		{"default", DefaultCoordinatorWaitTriggers(false), WaitTriggerNextWorkerDone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := waitMatchInput{Wake: anchor.WorkerBudgetRequested}
			if !waitEventMatches(tc.triggers, in) {
				t.Fatalf("budget request must wake a %s wait", tc.name)
			}
			cond, ok := waitConditionForWake(tc.triggers, in)
			if !ok || cond.Kind != string(tc.want) {
				t.Fatalf("condition = %+v ok=%v want %s", cond, ok, tc.want)
			}
		})
	}
	for _, triggers := range [][]WaitTrigger{
		{WaitTriggerTimer},
		{WaitTriggerTimer, WaitTriggerProcessDone},
		{WaitTriggerTimer, WaitTriggerScanDone},
	} {
		if waitEventMatches(triggers, waitMatchInput{Wake: anchor.WorkerBudgetRequested}) {
			t.Fatalf("budget request must not wake a wait on %v", triggers)
		}
	}
}

func TestNudgeWorkerBudgetRequestedBreaksWorkerSleepOnly(t *testing.T) {
	loop := NewLoopEngine()
	deps := loopDepsForTest()
	deps.GetSession = func(_ context.Context, sessionID string) (*api.Session, error) {
		return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
	}
	loop.SetDeps(deps)
	deadline := time.Now().UTC().Add(10 * time.Minute)
	loop.EnterSleep(context.Background(), "worker-waiter", deadline, "waiting for workers", []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}, nil, SleepMoverHost)
	loop.EnterSleep(context.Background(), "process-waiter", deadline, "waiting for a command", []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, nil, SleepMoverHost)

	loop.NudgeWorkerBudgetRequested(context.Background(), "worker-waiter", "job-1", budgetRequestEnvelope("job-1"))
	loop.NudgeWorkerBudgetRequested(context.Background(), "process-waiter", "job-1", budgetRequestEnvelope("job-1"))

	if loop.IsSleeping("worker-waiter") {
		t.Fatal("budget request must break a sleep on workers")
	}
	if !loop.IsSleeping("process-waiter") {
		t.Fatal("budget request must not break a sleep that awaits no worker")
	}
}
