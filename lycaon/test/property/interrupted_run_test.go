//go:build integration

package property

import (
	"context"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
	"testing"
)

// Orphaned non-ambient runs reconcile to one interrupted boundary.
func TestInterruptedRunReconcileEmitsTerminalBoundary(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "interrupted.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sessStore := store.NewSQL(sqlDB)
	wfStore := workflow.NewSQLStore(sqlDB)
	runMgr := workflow.NewManager(wfStore, sessStore, nil, nil)
	runMgr.SessionCoordinatorBusy = func(context.Context, string) bool { return false }

	rapid.Check(t, func(rt *rapid.T) {
		sess, err := sessStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		failErr(rt, "create session", err)

		runID := "run-" + rapid.StringMatching(`[a-z0-9]{6,10}`).Draw(rt, "run")
		phase := rapid.SampledFrom([]string{"boot", "review", "closeout"}).Draw(rt, "phase")
		failErr(rt, "create run", wfStore.CreateState(ctx, &api.WorkflowRun{
			ID:              runID,
			SessionID:       sess.ID,
			WorkflowID:      "plan",
			WorkflowVersion: "1.0.0",
			Status:          api.WorkflowRunStatusRunning,
			CurrentPhase:    phase,
		}, sess.WorkspacePath, nil))

		failErr(rt, "reconcile", runMgr.ReconcileOrphanedRuns(ctx, sess.ID))

		got, err := wfStore.Get(ctx, runID)
		failErr(rt, "get run", err)
		if got.Status != api.WorkflowRunStatusInterrupted {
			rt.Fatalf("status = %q want interrupted", got.Status)
		}
		msgs, err := sessStore.GetMessages(ctx, sess.ID)
		failErr(rt, "messages", err)
		var boundaries int
		for _, msg := range msgs {
			if msg.WorkflowBoundary != nil && msg.WorkflowBoundary.Event == string(api.WorkflowBoundaryKindInterrupted) {
				boundaries++
			}
		}
		if boundaries != 1 {
			rt.Fatalf("interrupted boundaries = %d want 1", boundaries)
		}
	})
}
