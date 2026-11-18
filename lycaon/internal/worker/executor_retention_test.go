package worker

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
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
	queue := newTestQueue(t, 1, testdbseed.DefaultProjectID, "parent-1")
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

func TestExecuteRetainsChildSessionForDenTranscript(t *testing.T) {
	ctx := context.Background()
	store := storetest.New(t, testdbseed.DefaultProjectID)

	var childID string
	runner := &fakePromptRunner{
		spawnChild: func(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error) {
			parent, err := store.Sessions.Get(ctx, parentID)
			if err != nil {
				return nil, err
			}
			child, err := store.Sessions.CreateChild(ctx, parent, req)
			if err != nil {
				return nil, err
			}
			childID = child.ID
			return child, nil
		},
	}
	binder := &fakeChildSessionBinder{}
	exec := NewLocalWorkerExecutor(runner, binder, runner, runner, runner, runner, runner, runner)
	exec.SetPromptInjects(promptstest.InjectRenderer(t))

	parent, err := store.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	queue := NewSQLQueue(store.DB(), 1)
	jobID, err := queue.EnqueueWithProjectID(ctx, parent.ProjectID, api.WorkerTask{
		ParentSessionID: parent.ID,
		Prompt:          "do work",
		Brief:           "fixture",
		AgentType:       "implementer",
	})
	testutil.FailErr(t, "enqueue worker", err)
	task, ok := queue.Get(jobID)
	if !ok {
		t.Fatal("enqueued worker missing")
	}
	_, err = exec.Execute(ctx, *task, WorkerRunContext{ProjectDir: parent.WorkspacePath})
	testutil.FailErr(t, "exec.Execute failed", err)
	if childID == "" {
		t.Fatal("child session id not captured")
	}
	if binder.jobID != jobID || binder.childID != childID {
		t.Fatalf("child binding = %q/%q", binder.jobID, binder.childID)
	}
	if _, err := store.Sessions.Get(ctx, childID); err != nil {
		t.Fatalf("child session removed after worker completion: %v", err)
	}
}
