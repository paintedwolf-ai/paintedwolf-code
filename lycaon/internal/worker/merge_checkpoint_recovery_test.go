package worker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
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
	cpStore := openRecoveryCheckpointStore(t)
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
	bindRecoveryCheckpoint(t, overlay, primary, cpStore, "refs-only")
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

// bindRecoveryCheckpoint names a published checkpoint as a branch's result, as completion does.
func bindRecoveryCheckpoint(t *testing.T, branch, source string, store *checkpoint.Store, checkpointID string) {
	t.Helper()
	meta := workspace.JobMeta{Roots: []workspace.JobMetaRoot{{ID: "primary", Path: source, IsPrimary: true}}, SnapshotComplete: true}
	meta.LastPublishedCheckpointID, meta.CheckpointStoreDir = checkpointID, store.RootDir()
	testutil.FailErr(t, "record branch checkpoint", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(filepath.Clean(branch)), meta))
}

func openRecoveryCheckpointStore(t *testing.T) *checkpoint.Store {
	t.Helper()
	store, err := checkpoint.NewStore(filepath.Join(enginepaths.WorkerRecoveryRootUnder(t.TempDir()), "checkpoints"))
	testutil.FailErr(t, "open recovery checkpoint store", err)
	return store
}

func TestCheckpointPromotionSingleStageCleanAndStage1Resolutions(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "store.db")
	store := NewSQLStore(sqlDB)
	queue := NewSQLQueue(sqlDB, 8)
	primary := t.TempDir()
	secondary := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, sqlDB, "parent-dedup", testdbseed.DefaultProjectID, primary)

	testutil.FailErr(t, "seed app.go", os.WriteFile(filepath.Join(primary, "app.go"), []byte("package app\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n"), 0o644))
	testutil.FailErr(t, "seed obsolete.txt", os.WriteFile(filepath.Join(primary, "obsolete.txt"), []byte("delete-me\n"), 0o644))
	testutil.FailErr(t, "seed exec.sh", os.WriteFile(filepath.Join(primary, "exec.sh"), []byte("#!/bin/sh\necho hi\n"), 0o644))
	testutil.FailErr(t, "seed replaced dir", os.MkdirAll(filepath.Join(primary, "replaced"), 0o755))
	testutil.FailErr(t, "seed replaced/child.txt", os.WriteFile(filepath.Join(primary, "replaced", "child.txt"), []byte("child\n"), 0o644))
	testutil.FailErr(t, "seed conflict.txt", os.WriteFile(filepath.Join(primary, "conflict.txt"), []byte("base-v1\n"), 0o644))

	jobID, err := queue.Enqueue(ctx, api.WorkerTask{
		ID: "job-cp-dedup", ParentSessionID: "parent-dedup", ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, AgentType: "implementer", Prompt: "dedup", Brief: "dedup stage1/stage2",
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
	})
	testutil.FailErr(t, "enqueue worker", err)
	t.Cleanup(func() { session.ClearTaskCheckpointPreview(jobID) })

	overlay := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", jobID)
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(overlay, 0o755))
	baseline := testbaseline.Durable(t, sqlDB, jobID, primary)
	cpStore := openRecoveryCheckpointStore(t)

	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-dedup", BackingDir: t.TempDir(),
		Roots: []layeredfs.RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true},
			{ID: "docs", Label: "docs", TrunkPath: secondary},
		},
	})
	testutil.FailErr(t, "new core", err)
	defer func() { _ = core.ReleaseView() }()

	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}, []byte("package app\n\nfunc A() int { return 1 }\n\nfunc B() int { return 22 }\n"), 0o644)
	testutil.FailErr(t, "worker app.go", err)
	rawBin := []byte{0xff, 0xfe, 0x00, 0x01, 0x80}
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "bin.dat"}, rawBin, 0o640)
	testutil.FailErr(t, "worker bin.dat", err)
	testutil.FailErr(t, "worker remove obsolete.txt", core.RemoveAll(layeredfs.QualifiedPath{RootID: "primary", Rel: "obsolete.txt"}))
	_, err = core.Chmod(layeredfs.QualifiedPath{RootID: "primary", Rel: "exec.sh"}, 0o755)
	testutil.FailErr(t, "worker chmod exec.sh", err)
	testutil.FailErr(t, "worker remove replaced dir", core.RemoveAll(layeredfs.QualifiedPath{RootID: "primary", Rel: "replaced"}))
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "replaced"}, []byte("now-a-file\n"), 0o644)
	testutil.FailErr(t, "worker write replaced file", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "conflict.txt"}, []byte("worker-v2\n"), 0o644)
	testutil.FailErr(t, "worker conflict.txt", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "docs", Rel: "guide.md"}, []byte("# Guide\n"), 0o644)
	testutil.FailErr(t, "worker @docs/guide.md", err)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: core, Store: cpStore, WorkerID: jobID, TaskID: jobID, ProjectID: testdbseed.DefaultProjectID,
	})
	testutil.FailErr(t, "publisher", err)
	_, err = pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "chk-dedup-1", Trigger: checkpoint.TriggerWorkerCompletion})
	testutil.FailErr(t, "publish", err)

	meta := workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{
			{ID: "primary", Label: "primary", Path: primary, IsPrimary: true},
			{ID: "docs", Label: "docs", Path: secondary},
		},
		SnapshotComplete:          true,
		LastPublishedCheckpointID: "chk-dedup-1",
		CheckpointStoreDir:        cpStore.RootDir(),
	}
	testutil.FailErr(t, "write job meta", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(filepath.Clean(overlay)), meta))
	_, err = store.SetWorkerWorkspace(ctx, jobID, overlay, baseline)
	testutil.FailErr(t, "bind worker", err)
	testutil.FailErr(t, "pending worker", queue.SetMergeStatus(ctx, jobID, api.WorkerMergeStatusPending))

	// Advance trunk on app.go (clean 3-way merge) and conflict.txt (overlapping conflict).
	testutil.FailErr(t, "trunk app.go", os.WriteFile(filepath.Join(primary, "app.go"), []byte("package app\n\nfunc A() int { return 10 }\n\nfunc B() int { return 2 }\n"), 0o644))
	testutil.FailErr(t, "trunk conflict.txt", os.WriteFile(filepath.Join(primary, "conflict.txt"), []byte("trunk-v2\n"), 0o644))

	svc := &MergeService{Queue: queue, Store: queue, DataDir: t.TempDir()}
	_, err = svc.PreviewForSession(ctx, "parent-dedup", jobID, "summary", nil)
	testutil.FailErr(t, "PreviewForSession", err)

	resInput := api.PromoteOverlayInput{
		Resolutions: []api.WorkerPromoteResolution{{Path: "conflict.txt", Action: api.WorkerPromoteResolutionActionTakeBranch}},
	}
	_, err = sqlDB.ExecContext(ctx, `CREATE TRIGGER reject_dedup_completion BEFORE UPDATE OF merge_status ON worker_jobs WHEN NEW.merge_status='merged' BEGIN SELECT RAISE(ABORT,'reject dedup completion'); END`)
	testutil.FailErr(t, "install trigger", err)
	if _, err := svc.PromoteOverlay(ctx, "parent-dedup", jobID, resInput); err == nil {
		t.Fatal("expected SQL completion trigger to fail PromoteOverlay")
	}
	// Verify full rollback of both Stage-1 resolution and Stage-2 checkpoint effects.
	if got, _ := os.ReadFile(filepath.Join(primary, "conflict.txt")); string(got) != "trunk-v2\n" {
		t.Fatalf("conflict.txt after rollback = %q, want trunk-v2", got)
	}
	if _, err := os.Stat(filepath.Join(primary, "obsolete.txt")); err != nil {
		t.Fatalf("obsolete.txt missing after rollback: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(secondary, "guide.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secondary guide.md survived rollback: %v", err)
	}

	_, err = sqlDB.ExecContext(ctx, `DROP TRIGGER reject_dedup_completion`)
	testutil.FailErr(t, "drop trigger", err)
	_, err = svc.PreviewForSession(ctx, "parent-dedup", jobID, "summary", nil)
	testutil.FailErr(t, "refresh PreviewForSession", err)
	out, err := svc.PromoteOverlay(ctx, "parent-dedup", jobID, resInput)
	testutil.FailErr(t, "PromoteOverlay retry", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status = %q, want merged", out.Status)
	}
	if got, err := os.ReadFile(filepath.Join(primary, "app.go")); err != nil || string(got) != "package app\n\nfunc A() int { return 10 }\n\nfunc B() int { return 22 }\n" {
		t.Fatalf("merged app.go = %q (err=%v)", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(primary, "bin.dat")); err != nil || !bytes.Equal(got, rawBin) {
		t.Fatalf("bin.dat = %x (err=%v), want %x", got, err, rawBin)
	}
	if _, err := os.Lstat(filepath.Join(primary, "obsolete.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete.txt survived delete promotion: %v", err)
	}
	if info, err := os.Stat(filepath.Join(primary, "exec.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("exec.sh mode = %v (err=%v), want 0755", info, err)
	}
	if got, err := os.ReadFile(filepath.Join(primary, "replaced")); err != nil || string(got) != "now-a-file\n" {
		t.Fatalf("structural replaced = %q (err=%v), want now-a-file", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(primary, "conflict.txt")); err != nil || string(got) != "worker-v2\n" {
		t.Fatalf("resolved conflict.txt = %q (err=%v), want worker-v2", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(secondary, "guide.md")); err != nil || string(got) != "# Guide\n" {
		t.Fatalf("secondary guide.md = %q (err=%v)", got, err)
	}
}
