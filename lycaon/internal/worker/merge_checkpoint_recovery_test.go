package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEmptyCheckpointPromotionRollsBackRefsOnCommitFailure(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "store.db")
	store := NewSQLStore(sqlDB)
	queue := NewSQLQueue(sqlDB, 8)
	primary := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, sqlDB, "parent", testdbseed.DefaultProjectID, primary)
	id, err := queue.Enqueue(ctx, api.WorkerTask{ID: "refs-only", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, AgentType: "implementer", Prompt: "checkpoint", Brief: "retain refs", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}})
	testutil.FailErr(t, "enqueue worker", err)
	t.Cleanup(func() { session.ClearTaskCheckpointPreview(id) })
	overlay := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", id)
	baseline := testbaseline.Durable(t, sqlDB, id, primary)
	cpStore, err := checkpoint.NewStore(filepath.Join(filepath.Dir(baseline), session.TaskCheckpointStoreSubdir))
	testutil.FailErr(t, "checkpoint store", err)
	core, err := layeredfs.New(layeredfs.Config{ViewID: "refs-only", BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}}})
	testutil.FailErr(t, "worker view", err)
	defer func() { _ = core.ReleaseView() }()
	testutil.FailErr(t, "target object directory", os.MkdirAll(filepath.Join(primary, ".git/objects"), 0755))
	oid, err := checkpoint.WriteLooseGitObject(filepath.Join(cpStore.RootDir(), "objects"), "blob", []byte("retained object"))
	testutil.FailErr(t, "checkpoint object", err)
	publisher, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{Core: core, Store: cpStore, WorkerID: id, TaskID: id, ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "publisher", err)
	_, err = publisher.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "refs-only", Trigger: checkpoint.TriggerWorkerCompletion, PrivateGit: checkpoint.PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feature": oid}}})
	testutil.FailErr(t, "publish refs checkpoint", err)
	_, err = store.SetWorkerWorkspace(ctx, id, overlay, baseline)
	testutil.FailErr(t, "bind worker", err)
	testutil.FailErr(t, "pending worker", queue.SetMergeStatus(ctx, id, api.WorkerMergeStatusPending))
	svc := &MergeService{Queue: queue, Store: queue, DataDir: t.TempDir()}
	_, err = sqlDB.ExecContext(ctx, `CREATE TRIGGER reject_refs_completion BEFORE UPDATE OF merge_status ON worker_jobs WHEN NEW.merge_status='merged' BEGIN SELECT RAISE(ABORT,'reject refs completion'); END`)
	testutil.FailErr(t, "completion fault", err)
	if _, err = svc.PromoteOverlay(ctx, "parent", id, api.PromoteOverlayInput{}); err == nil {
		t.Fatal("refs-only promotion ignored SQL failure")
	}
	for _, ref := range []string{"refs/lycaon/workers/refs-only/heads/feature", "refs/lycaon/checkpoints/refs-only/heads/feature"} {
		if _, err := os.Lstat(filepath.Join(primary, ".git", ref)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refs-only rollback retained %s: %v", ref, err)
		}
	}
	if task, ok := queue.Get(id); !ok || task.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("refs-only rollback status=%+v", task)
	}
	_, err = sqlDB.ExecContext(ctx, `DROP TRIGGER reject_refs_completion`)
	testutil.FailErr(t, "remove completion fault", err)
	result, err := svc.PromoteOverlay(ctx, "parent", id, api.PromoteOverlayInput{})
	testutil.FailErr(t, "retry refs-only promotion", err)
	if result.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("refs-only retry status=%s", result.Status)
	}
}
