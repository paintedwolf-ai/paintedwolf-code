package worker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBeginMergeApplyClaimsPendingOnce(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "store.db")
	store := NewSQLStore(sqlDB)
	q := NewSQLQueue(sqlDB, 8)
	dir := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, sqlDB, "parent-1", testdbseed.DefaultProjectID, dir)
	id, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: "parent-1",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Prompt:          "write",
		Brief:           "Update a.go",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}},
	})
	testutil.FailErr(t, "Enqueue", err)
	bound, err := store.SetWorkerWorkspace(ctx, id, filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", id), testbaseline.Durable(t, sqlDB, id, t.TempDir()))
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	if !bound {
		t.Fatal("expected workspace root CAS to win on an unbound job")
	}
	testutil.FailErr(t, "SetMergeStatus", q.SetMergeStatus(ctx, id, api.WorkerMergeStatusPending))

	token, ok, err := q.BeginMergeApply(ctx, id)
	testutil.FailErr(t, "BeginMergeApply first", err)
	if !ok || token == "" {
		t.Fatalf("expected first claim with token, got ok=%v token=%q", ok, token)
	}
	_, ok, err = q.BeginMergeApply(ctx, id)
	testutil.FailErr(t, "BeginMergeApply second", err)
	if ok {
		t.Fatal("expected second claim to fail while applying")
	}
	task, found := q.Get(id)
	if !found || task.MergeStatus != api.WorkerMergeStatusApplying {
		t.Fatalf("status = %q found=%v", task.MergeStatus, found)
	}
}

func TestMergeApplyLeaseLossCancelsActiveWriter(t *testing.T) {
	store := obligationTestStore(t)
	oldToken := insertApplyingWorker(t, store, "job-lease-loss", t.TempDir())
	testutil.FailErr(t, "release setup claim", store.ReleaseMergeApply(t.Context(), "job-lease-loss", oldToken))

	previousInterval := mergeApplyHeartbeatInterval
	mergeApplyHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { mergeApplyHeartbeatInterval = previousInterval })
	service := &MergeService{Store: store}
	claim, claimCtx, err := service.beginMergeClaim(t.Context(), "job-lease-loss")
	testutil.FailErr(t, "begin merge claim", err)
	t.Cleanup(claim.stop)
	_, err = store.db.ExecContext(t.Context(), `
		UPDATE worker_jobs SET merge_claim_token = 'replacement-claim' WHERE id = 'job-lease-loss'`)
	testutil.FailErr(t, "replace merge claim", err)

	select {
	case <-claimCtx.Done():
		if !errors.Is(context.Cause(claimCtx), errMergeApplyClaimLost) {
			t.Fatalf("claim cause = %v", context.Cause(claimCtx))
		}
	case <-time.After(time.Second):
		t.Fatal("active writer was not canceled after losing its merge lease")
	}
	if err := store.ReleaseMergeApply(t.Context(), "job-lease-loss", claim.token); !errors.Is(err, errMergeApplyClaimLost) {
		t.Fatalf("stale claim release = %v", err)
	}
}
