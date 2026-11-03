package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func seedSandbox(t *testing.T, sandboxRoot, primary, jobID string) string {
	t.Helper()
	dir := enginepaths.JobBranchDir(sandboxRoot, primary, jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir sandbox", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "touch.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write sandbox file", err)
	}
	return dir
}

func retainedJobIDs(ids map[string]struct{}) func(context.Context) (map[string]struct{}, error) {
	return func(context.Context) (map[string]struct{}, error) { return ids, nil }
}

func TestReconcileStaleSandboxesRemovesOrphans(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()
	stale := seedSandbox(t, sandboxRoot, primary, "stale-id")
	keep := seedSandbox(t, sandboxRoot, primary, "keep-id")

	retain := map[string]struct{}{"keep-id": {}}
	removed, err := workspace.ReconcileStaleSandboxes(context.Background(), sandboxRoot, primary, retainedJobIDs(retain))
	testutil.FailErr(t, "ReconcileStaleSandboxes", err)
	if removed != 1 {
		t.Fatalf("removed = %d want 1", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale sandbox should be removed")
	}
	if _, err := os.Stat(keep); err != nil {
		testutil.FailErr(t, "retained sandbox", err)
	}
}

func TestReconcileStaleSandboxesIgnoresConcurrentCreate(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()
	stale := seedSandbox(t, sandboxRoot, primary, "stale-id")
	created := ""

	removed, err := workspace.ReconcileStaleSandboxes(
		context.Background(), sandboxRoot, primary,
		func(context.Context) (map[string]struct{}, error) {
			created = seedSandbox(t, sandboxRoot, primary, "new-id")
			return nil, nil
		},
	)
	testutil.FailErr(t, "ReconcileStaleSandboxes", err)
	if removed != 1 {
		t.Fatalf("removed = %d want only the snapshotted stale sandbox", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("snapshotted stale sandbox should be removed")
	}
	if _, err := os.Stat(created); err != nil {
		testutil.FailErr(t, "concurrently created sandbox", err)
	}
}

func TestReconcileStaleSandboxesScopedPerProject(t *testing.T) {
	sandboxRoot := t.TempDir()
	projA := t.TempDir()
	projB := t.TempDir()
	other := seedSandbox(t, sandboxRoot, projB, "b-job")
	seedSandbox(t, sandboxRoot, projA, "a-job")

	removed, err := workspace.ReconcileStaleSandboxes(context.Background(), sandboxRoot, projA, retainedJobIDs(nil))
	testutil.FailErr(t, "ReconcileStaleSandboxes", err)
	if removed != 1 {
		t.Fatalf("removed = %d want 1 (project A only)", removed)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("sibling project sandbox must be untouched")
	}
}

func seedSandboxPair(t *testing.T, sandboxRoot, primary, jobID string) (jobDir, metaDir string) {
	t.Helper()
	jobDir = seedSandbox(t, sandboxRoot, primary, jobID)
	metaDir = enginepaths.MetaDirForBranchRoot(jobDir)
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		testutil.FailErr(t, "mkdir meta", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "state.json"), []byte(`{"strategy":"direct_copy"}`), 0o600); err != nil {
		testutil.FailErr(t, "write meta", err)
	}
	return jobDir, metaDir
}

func TestReconcileStaleSandboxesRemovesJobAndMetaTogether(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()
	staleJob, staleMeta := seedSandboxPair(t, sandboxRoot, primary, "stale-id")
	keepJob, keepMeta := seedSandboxPair(t, sandboxRoot, primary, "keep-id")

	removed, err := workspace.ReconcileStaleSandboxes(context.Background(), sandboxRoot, primary, retainedJobIDs(map[string]struct{}{"keep-id": {}}))
	testutil.FailErr(t, "ReconcileStaleSandboxes", err)
	if removed != 1 {
		t.Fatalf("removed = %d want 1", removed)
	}
	if _, err := os.Stat(staleJob); !os.IsNotExist(err) {
		t.Fatal("stale job tree should be removed")
	}
	if _, err := os.Stat(staleMeta); !os.IsNotExist(err) {
		t.Fatal("stale job meta should be removed with the tree")
	}
	if _, err := os.Stat(keepJob); err != nil {
		testutil.FailErr(t, "retained job", err)
	}
	if _, err := os.Stat(keepMeta); err != nil {
		testutil.FailErr(t, "retained meta", err)
	}
}

func TestReconcileStaleSandboxesDoesNotTreatMetaAsJob(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()
	job, meta := seedSandboxPair(t, sandboxRoot, primary, "81157094-653f-4496-b675-dcf887c2d4b4")

	removed, err := workspace.ReconcileStaleSandboxes(context.Background(), sandboxRoot, primary, retainedJobIDs(map[string]struct{}{
		"81157094-653f-4496-b675-dcf887c2d4b4": {},
	}))
	testutil.FailErr(t, "ReconcileStaleSandboxes", err)
	if removed != 0 {
		t.Fatalf("removed = %d want 0 — live meta is not a job", removed)
	}
	if _, err := os.Stat(job); err != nil {
		testutil.FailErr(t, "live job", err)
	}
	if _, err := os.Stat(meta); err != nil {
		testutil.FailErr(t, "live meta", err)
	}
}

func TestReconcileStaleSandboxesRemovesOrphanMeta(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()
	job := seedSandbox(t, sandboxRoot, primary, "gone-job")
	meta := enginepaths.MetaDirForBranchRoot(job)
	if err := os.MkdirAll(meta, 0o700); err != nil {
		testutil.FailErr(t, "mkdir meta", err)
	}
	if err := os.RemoveAll(job); err != nil {
		testutil.FailErr(t, "remove job tree", err)
	}

	removed, err := workspace.ReconcileStaleSandboxes(context.Background(), sandboxRoot, primary, retainedJobIDs(nil))
	testutil.FailErr(t, "ReconcileStaleSandboxes", err)
	if removed != 1 {
		t.Fatalf("removed = %d want 1 orphan meta", removed)
	}
	if _, err := os.Stat(meta); !os.IsNotExist(err) {
		t.Fatal("orphan meta should be removed")
	}
}

func TestReconcileOwnedWorkerViewsSettlesOnlyUnretainedRecordedIdentities(t *testing.T) {
	cpStore, err := checkpoint.NewStore(t.TempDir())
	testutil.FailErr(t, "NewStore", err)
	coord, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir:         t.TempDir(),
		CheckpointStore: cpStore,
	})
	testutil.FailErr(t, "NewCoordinator", err)

	for _, w := range []struct {
		id, view string
	}{
		{"job-keep", "view-keep"},
		{"job-stale", "view-stale"},
	} {
		testutil.FailErr(t, "SaveWorkerJournal", coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
			FormatVersion: recovery.FormatVersion,
			WorkerID:      w.id,
			ViewID:        w.view,
			TaskID:        "t-1",
			ProjectID:     "p-1",
			AdapterID:     adapter.AdapterFUSELinux,
		}))
		testutil.FailErr(t, "RecordMountIdentity", coord.RecordMountIdentity(recovery.MountIdentityRecord{
			ViewID:         w.view,
			WorkerID:       w.id,
			AdapterID:      adapter.AdapterFUSELinux,
			RuntimeViewDir: t.TempDir(),
			BackingDir:     t.TempDir(),
		}))
	}

	settled, err := workspace.ReconcileOwnedWorkerViews(
		t.Context(), coord, []string{"job-keep", "job-stale"}, map[string]struct{}{"job-keep": {}},
	)
	testutil.FailErr(t, "ReconcileOwnedWorkerViews", err)
	if settled != 1 {
		t.Fatalf("settled = %d want 1", settled)
	}
	if _, ok, _ := coord.LoadMountIdentity("job-keep", "view-keep"); !ok {
		t.Fatal("expected retained job-keep mount identity untouched")
	}
	if _, ok, _ := coord.LoadMountIdentity("job-stale", "view-stale"); ok {
		t.Fatal("expected stale job-stale mount identity settled and removed")
	}
}
