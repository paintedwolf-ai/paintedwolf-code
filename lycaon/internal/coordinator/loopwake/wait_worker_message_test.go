package loopwake

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

// A message that needs a reply resumes a wait on its worker or on all
// workers, ending it with the message rather than as satisfied.
func TestWorkerMessageResumesWorkerWaits(t *testing.T) {
	named := []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}
	in := waitMatchInput{Wake: anchor.WorkerMessageReceived, Subject: "job-1", WorkerHandles: []string{"job-1"}}
	cond, ok := waitConditionForWake(named, in)
	if !waitEventMatches(named, in) || !ok || cond.Kind != string(WaitTriggerNextWorkerDone) || cond.Outcome != "message" || len(cond.Handles) != 1 || cond.Handles[0] != "job-1" {
		t.Fatalf("condition = %+v ok=%v want a message outcome for job-1", cond, ok)
	}
	if waitEventMatches(named, waitMatchInput{Wake: anchor.WorkerMessageReceived, Subject: "job-2", WorkerHandles: []string{"job-1"}}) {
		t.Fatal("message from another worker woke a wait on job-1")
	}
	all := []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}
	if cond, ok := waitConditionForWake(all, waitMatchInput{Wake: anchor.WorkerMessageReceived, Subject: "job-2"}); !ok || cond.Outcome != "message" {
		t.Fatalf("all-workers condition = %+v ok=%v", cond, ok)
	}
	if waitEventMatches([]WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, waitMatchInput{Wake: anchor.WorkerMessageReceived, Subject: "job-1"}) {
		t.Fatal("message woke a wait on a process")
	}
}

// The message itself reaches the coordinator on its next request, so the
// wake queues no inform of its own.
func TestNudgeWorkerMessageBreaksWorkerSleepWithoutInform(t *testing.T) {
	loop := NewLoopEngine()
	deps := loopDepsForTest()
	deps.GetSession = func(_ context.Context, sessionID string) (*api.Session, error) {
		return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
	}
	var informs []anchor.ID
	deps.QueueInform = func(_ context.Context, _ string, inform anchor.ID, _ anchor.Envelope) {
		informs = append(informs, inform)
	}
	loop.SetDeps(deps)
	deadline := time.Now().UTC().Add(10 * time.Minute)
	loop.Waits.EnterSleep(context.Background(), "worker-waiter", deadline, "waiting for workers", []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}, nil, SleepMoverHost)
	loop.Waits.EnterSleep(context.Background(), "process-waiter", deadline, "waiting for a command", []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, nil, SleepMoverHost)

	loop.Nudges.NudgeWorkerMessage(context.Background(), "worker-waiter", "job-1")
	loop.Nudges.NudgeWorkerMessage(context.Background(), "process-waiter", "job-1")

	if loop.Waits.IsSleeping("worker-waiter") {
		t.Fatal("a message needing a reply must break a sleep on workers")
	}
	if !loop.Waits.IsSleeping("process-waiter") {
		t.Fatal("a message must not break a sleep that awaits no worker")
	}
	if len(informs) != 0 {
		t.Fatalf("message wake queued informs %v", informs)
	}
}
