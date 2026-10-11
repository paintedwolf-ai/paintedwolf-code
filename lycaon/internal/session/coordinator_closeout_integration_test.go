//go:build integration

package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func requireWorkflowCloseoutRejection(t *testing.T, err error) {
	t.Helper()
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || refusal.Code() != "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT" || !errors.Is(err, guidance.ErrCloseoutStopped) {
		t.Fatalf("gated prose closeout = %v; want structured workflow gate stop", err)
	}
}

func TestLegFinishedQueuesKickAndAutoPrompts(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()
	queue := worker.NewInMemoryQueue(4)
	fix.mgr.SetWorkerQueue(queue)
	child, err := fix.store.CreateChild(ctx, fix.sess, wire.SpawnChildRequest{AgentType: orchestration.ProfilePathExplorer})
	testutil.FailErr(t, "create child", err)
	jobID, err := queue.Enqueue(ctx, wire.WorkerTask{
		ParentSessionID: fix.sess.ID, ChildSessionID: child.ID, ProjectID: fix.sess.ProjectID,
		WorkspacePath: fix.sess.WorkspacePath, AgentType: orchestration.ProfilePathExplorer,
		Prompt: "research the task", Brief: "fixture", DelegationID: "dep-1", LegID: "leg-1",
	})
	testutil.FailErr(t, "enqueue research leg", err)
	claimed, err := queue.ClaimNext(ctx, worker.ClaimRequest{ExecutionTarget: wire.ExecutionTargetLocal})
	testutil.FailErr(t, "claim research leg", err)
	if claimed.ID != jobID {
		t.Fatalf("claimed job = %s want %s", claimed.ID, jobID)
	}
	_, err = fix.mgr.Submissions.Prompt(ctx, fix.sess.ID, "start implement")
	testutil.FailErr(t, "park prompt with active research worker", err)
	before := len(fix.rec.AllRequests())
	_, err = fix.mgr.Workers.Summaries.Append(ctx, fix.sess.ID, workeroutcomes.SummaryInput{
		Summary: "leg done", DelegationID: "dep-1", LegID: "leg-1", JobID: jobID,
		ChildSessionID: child.ID, AgentType: orchestration.ProfilePathExplorer,
		Status: "complete", CompletedAt: autoContinuePtrTime(time.Now().UTC()),
	})
	testutil.FailErr(t, "append leg summary", err)
	simulateWorkerJobComplete(t, fix.mgr, queue, jobID)
	testutil.WaitFor(t, 5*time.Second, func() bool {
		msgs, err := fix.store.GetMessages(ctx, fix.sess.ID)
		if err != nil {
			return false
		}
		for _, msg := range msgs {
			if msg.Kind == wire.MessageKindHostLoopWake && len(fix.rec.AllRequests()) > before {
				return true
			}
		}
		return false
	})
}
