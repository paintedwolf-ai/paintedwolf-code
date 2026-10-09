package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestResumeParentAfterChildExitEmitsResumedBoundary(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	parent := &api.WorkflowRun{
		ID: "run-parent", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusPausedOnChild, CurrentPhase: "boot",
	}
	testutil.FailErr(t, "create parent", mgr.Store.State.CreateState(ctx, parent, "", nil))
	parentID := parent.ID
	child := &api.WorkflowRun{
		ID: "run-child", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", ParentRunID: &parentID,
		Status: api.WorkflowRunStatusCanceled, CurrentPhase: "boot", CompletedAt: new(time.Now().UTC()),
	}
	testutil.FailErr(t, "create child", mgr.Store.State.CreateState(ctx, child, "", nil))

	gotParent, err := mgr.Children.ResumeParentAfterChildExit(ctx, child, string(child.Status))
	testutil.FailErr(t, "resumeParentAfterChildExit", err)
	if gotParent == nil || gotParent.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("parent status = %v want running", gotParent)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "messages", err)
	for _, msg := range msgs {
		if msg.WorkflowBoundary != nil && msg.WorkflowBoundary.Event == "resumed" {
			return
		}
	}
	t.Fatal("expected resumed boundary after parent leaves paused_on_child")
}
