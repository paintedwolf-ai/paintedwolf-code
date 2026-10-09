package phases_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

// TerminalChildRunForTest completes a child and resumes its parent.
func TerminalChildRunForTest(ctx context.Context, t *testing.T, mgr *workflow.RunManager, childID string) {
	t.Helper()
	child, err := mgr.Store.Runs.Get(ctx, childID)
	testutil.FailErr(t, "Get child run", err)
	now := time.Now().UTC()
	child.Status = api.WorkflowRunStatusComplete
	child.CompletedAt = &now
	child.UpdatedAt = now
	if err := mgr.Store.State.Update(ctx, child); err != nil {
		testutil.FailErr(t, "Update child run", err)
	}
	if err := mgr.Children.ReconcileTerminalRun(ctx, child); err != nil {
		testutil.FailErr(t, "ReconcileTerminalRun", err)
	}
}
