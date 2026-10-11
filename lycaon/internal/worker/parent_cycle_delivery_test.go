package worker

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type cycleDeliverySessions struct {
	*proofCountSessions
	queue            WorkerQueue
	notified         bool
	pendingAtRelease int
	readError        error
}

func (s *cycleDeliverySessions) Terminal(ctx context.Context, _, _ string) {
	items, err := s.queue.ListPendingOutcomes(ctx)
	s.pendingAtRelease, s.readError = len(items), err
	s.notified = true
}

func TestWorkerBridgeReleasesParentAfterQueueAcknowledgement(t *testing.T) {
	queue := NewInMemoryQueue(1)
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent-1",
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)
	completed, err := queue.Complete(t.Context(), claimed, api.WorkerResult{Status: "complete"})
	testutil.FailErr(t, "complete worker", err)
	if !completed {
		t.Fatal("worker completion lost its claim")
	}
	task, ok := queue.Get(id)
	if !ok {
		t.Fatal("completed worker missing")
	}
	sessions := &cycleDeliverySessions{proofCountSessions: &proofCountSessions{}, queue: queue}
	bridge := &SessionOutcomeBridge{Workers: sessions, Loop: sessions, Results: sessions, State: sessions, Closure: sessions}
	poller := NewLocalWorkerPoller(queue, panicExecutor{}, DefaultWorkersConfig(), bridge)
	testutil.FailErr(t, "deliver committed outcome", poller.deliverOutcome(t.Context(), *task))
	testutil.FailErr(t, "read outcomes during parent release", sessions.readError)
	if !sessions.notified || sessions.pendingAtRelease != 0 {
		t.Fatalf("parent release: notified=%v pending outcomes=%d", sessions.notified, sessions.pendingAtRelease)
	}
}
