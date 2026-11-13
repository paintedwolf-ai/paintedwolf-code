package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

// A round notice is about a worker, so every wait on workers wakes for it and
// no other wait does.
func TestWorkerRoundNoticeWakesEveryWorkerWait(t *testing.T) {
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
			in := waitMatchInput{Wake: anchor.WorkerRoundNotice, Subject: "job-1"}
			if !waitEventMatches(tc.triggers, in) {
				t.Fatalf("round notice must wake a %s wait", tc.name)
			}
			cond, ok := waitConditionForWake(tc.triggers, in)
			// The worker is still running: the wait ends with a notice about it.
			if !ok || cond.Kind != string(tc.want) || cond.Outcome != "notice" || len(cond.Handles) != 1 || cond.Handles[0] != "job-1" {
				t.Fatalf("condition = %+v ok=%v want %s notice for job-1", cond, ok, tc.want)
			}
		})
	}
	for _, triggers := range [][]WaitTrigger{
		{WaitTriggerTimer},
		{WaitTriggerTimer, WaitTriggerProcessDone},
		{WaitTriggerTimer, WaitTriggerScanDone},
	} {
		if waitEventMatches(triggers, waitMatchInput{Wake: anchor.WorkerRoundNotice}) {
			t.Fatalf("round notice must not wake a wait on %v", triggers)
		}
	}
}

// A wait on named workers wakes only for notices about them.
func TestWorkerRoundNoticeRespectsWaitedHandles(t *testing.T) {
	named := []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}
	if waitEventMatches(named, waitMatchInput{Wake: anchor.WorkerRoundNotice, Subject: "job-2", WorkerHandles: []string{"job-1"}}) {
		t.Fatal("notice about another worker woke a wait on job-1")
	}
	if !waitEventMatches(named, waitMatchInput{Wake: anchor.WorkerRoundNotice, Subject: "job-1", WorkerHandles: []string{"job-1"}}) {
		t.Fatal("notice about the waited worker did not wake its wait")
	}
	both := []WaitTrigger{WaitTriggerNextWorkerDone, WaitTriggerAllWorkersIdle}
	cond, ok := waitConditionForWake(both, waitMatchInput{Wake: anchor.WorkerRoundNotice, Subject: "job-2", WorkerHandles: []string{"job-1"}})
	if !ok || cond.Kind != string(WaitTriggerAllWorkersIdle) {
		t.Fatalf("condition = %+v ok=%v want the all-workers wait", cond, ok)
	}
}

func TestNudgeWorkerRoundNoticeBreaksWorkerSleepOnly(t *testing.T) {
	loop := NewLoopEngine()
	deps := loopDepsForTest()
	deps.GetSession = func(_ context.Context, sessionID string) (*api.Session, error) {
		return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
	}
	queued := map[string]string{}
	deps.QueueInform = func(_ context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
		if inform == anchor.WorkerRoundNotice {
			queued[sessionID] = env.Subject
		}
	}
	loop.SetDeps(deps)
	deadline := time.Now().UTC().Add(10 * time.Minute)
	loop.Waits.EnterSleep(context.Background(), "worker-waiter", deadline, "waiting for workers", []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}, nil, SleepMoverHost)
	loop.Waits.EnterSleep(context.Background(), "process-waiter", deadline, "waiting for a command", []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, nil, SleepMoverHost)

	loop.Nudges.NudgeWorkerRoundNotice(context.Background(), "worker-waiter", "job-1", roundNoticeEnvelope("job-1"))
	loop.Nudges.NudgeWorkerRoundNotice(context.Background(), "process-waiter", "job-1", roundNoticeEnvelope("job-1"))

	if loop.Waits.IsSleeping("worker-waiter") {
		t.Fatal("round notice must break a sleep on workers")
	}
	if !loop.Waits.IsSleeping("process-waiter") {
		t.Fatal("round notice must not break a sleep that awaits no worker")
	}
	// The wake that ends a wait carries no notice text, so the notice is queued
	// for both sessions whether or not their wait consumed the wake.
	if queued["worker-waiter"] != "job-1" || queued["process-waiter"] != "job-1" {
		t.Fatalf("queued notices = %v, want job-1 for both sessions", queued)
	}
}
