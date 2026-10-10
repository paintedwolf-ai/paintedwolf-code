package worker

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestCompletedOverlaySurvivesTreeEviction runs the whole cycle: a write
// worker completes, its overlay is sealed, retention reclaims the tree, and
// the merge still lands because the branch is rebuilt on demand.
func TestCompletedOverlaySurvivesTreeEviction(t *testing.T) {
	for _, relocate := range []bool{false, true} {
		name := "eviction"
		if relocate {
			name = "relocated archive"
		}
		t.Run(name, func(t *testing.T) { testCompletedOverlaySurvivesTreeEviction(t, relocate) })
	}
}

func testCompletedOverlaySurvivesTreeEviction(t *testing.T, relocate bool) {
	ctx := context.Background()
	project := t.TempDir()
	testutil.FailErr(t, "seed main", os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "seed keep", os.WriteFile(filepath.Join(project, "keep.txt"), []byte("keep\n"), 0o644))
	testutil.FailErr(t, "seed gone", os.WriteFile(filepath.Join(project, "gone.txt"), []byte("gone\n"), 0o644))
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "root-id", project)

	branches := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches")
	mgr := workspace.NewManager(branches, t.TempDir())
	q := NewSQLQueue(sqlDB, 4)
	q.SetBaselineStore(sourceledger.New(sqlDB, filepath.Join(testbaseline.DataDir(t, sqlDB), "source-content")).Baselines)
	q.SetWorkerWorkspaceManager(mgr)

	id, err := q.Enqueue(ctx, api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: project, WorkspaceRootID: "root-id",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
		AgentType: "implementer", Prompt: "Change main.go", Brief: "Change main.go",
	})
	testutil.FailErr(t, "enqueue", err)
	provisioning, err := q.ListBranchJobs(ctx)
	testutil.FailErr(t, "list branch ownership before workspace publication", err)
	if len(provisioning) != 1 || provisioning[0].ID != id || provisioning[0].Sealed {
		t.Fatalf("pending branch ownership = %+v", provisioning)
	}
	claimed, err := q.ClaimNext(ctx, ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim next", err)
	claimed, err = q.ClaimWorkerBranch(ctx, claimed.ID)
	testutil.FailErr(t, "claim branch", err)
	root := claimed.WorkspaceRoot
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(root, "main.go"), []byte("package worker\n"), 0o644))
	testutil.FailErr(t, "add", os.WriteFile(filepath.Join(root, "added.go"), []byte("package added\n"), 0o644))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(root, "gone.txt")))

	committed, err := q.Complete(ctx, claimed, api.WorkerResult{Status: "complete", Summary: "done"})
	testutil.FailErr(t, "complete", err)
	if !committed {
		t.Fatal("completion was not committed")
	}
	task, ok := q.Get(id)
	if !ok || task.MergeStatus != api.WorkerMergeStatusPending || task.WorkspaceOverlayPath == "" {
		t.Fatalf("task after completion = %+v", task)
	}
	jobs, err := q.ListBranchJobs(ctx)
	testutil.FailErr(t, "list branch jobs", err)
	if len(jobs) != 1 || !jobs[0].Sealed || jobs[0].ID != id {
		t.Fatalf("branch jobs = %+v", jobs)
	}
	wantChanged, err := session.InspectOverlayChanges(ctx, task, nil)
	testutil.FailErr(t, "changes before eviction", err)

	if relocate {
		q, mgr = relocateWorkerInstallation(t, sqlDB, id)
		task, ok = q.Get(id)
		if !ok {
			t.Fatal("restored worker missing")
		}
		if task.WorkspaceRoot == root {
			t.Fatal("restored branch still names the original installation")
		}
		root = task.WorkspaceRoot
	} else {
		testutil.FailErr(t, "evict", workspace.EvictJobTree(ctx, root))
	}
	if workspace.BranchTreePresent(root) {
		t.Fatal("tree should be gone")
	}
	if roots := TaskRootRefs(ctx, task, nil); len(roots) != 1 || roots[0].Path != project {
		t.Fatalf("roots after eviction = %+v", roots)
	}

	_, lease, err := q.EnsureWorkerBranch(ctx, id)
	testutil.FailErr(t, "ensure branch", err)
	body, err := os.ReadFile(filepath.Join(root, "main.go"))
	testutil.FailErr(t, "read rebuilt", err)
	if string(body) != "package worker\n" {
		t.Fatalf("rebuilt main.go = %q", body)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file came back: %v", err)
	}
	gotChanged, err := session.InspectOverlayChanges(ctx, task, nil)
	testutil.FailErr(t, "changes after rebuild", err)
	if len(gotChanged) != len(wantChanged) {
		t.Fatalf("changes after rebuild = %v want %v", gotChanged, wantChanged)
	}
	for i := range gotChanged {
		if gotChanged[i] != wantChanged[i] {
			t.Fatalf("changes after rebuild = %v want %v", gotChanged, wantChanged)
		}
	}
	if err := workspace.EvictJobTree(ctx, root); err == nil {
		t.Fatal("eviction must wait for the lease")
	}
	lease.Release()
	testutil.FailErr(t, "evict again", workspace.EvictJobTree(ctx, root))

	svc := &MergeService{Queue: q, Store: q, Workspace: mgr, DataDir: t.TempDir()}
	out, err := svc.PromoteOverlay(ctx, "", id, api.PromoteOverlayInput{})
	testutil.FailErr(t, "promote after eviction", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("promote status = %q conflicts=%+v", out.Status, out.Conflicts)
	}
	merged, err := os.ReadFile(filepath.Join(project, "main.go"))
	testutil.FailErr(t, "read merged", err)
	if string(merged) != "package worker\n" {
		t.Fatalf("merged main.go = %q", merged)
	}
	if _, err := os.Stat(filepath.Join(project, "added.go")); err != nil {
		t.Fatalf("added file not promoted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file not promoted: %v", err)
	}
	if workspace.BranchTreePresent(root) {
		t.Fatal("merged overlay must release its tree")
	}
}

func relocateWorkerInstallation(t *testing.T, original db.Handle, jobID string) (*SQLQueue, *workspace.Manager) {
	t.Helper()
	ctx := t.Context()
	oldRoot := testbaseline.DataDir(t, original)
	archive := filepath.Join(t.TempDir(), "installation.zip")
	_, err := backup.Create(ctx, backup.CreateOpts{ConfigDir: oldRoot, DBPath: filepath.Join(oldRoot, "store.db"), SQLDB: original, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}, archive)
	testutil.FailErr(t, "archive retained worker", err)
	testutil.FailErr(t, "close original installation", original.Close())
	testutil.FailErr(t, "remove original installation", os.RemoveAll(oldRoot))
	newRoot := t.TempDir()
	_, err = backup.Stage(ctx, backup.StageOpts{ConfigDir: newRoot, ArchivePath: archive, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage relocated worker archive", err)
	testutil.FailErr(t, "apply relocated worker archive", backup.ApplyPending(t.Context(), newRoot))
	restored := testdbfixture.OpenPath(t, filepath.Join(newRoot, "store.db"))
	var baselineID, branchRel string
	var overlayID sql.NullString
	testutil.FailErr(t, "read durable identities", restored.QueryRowContext(ctx, `SELECT workspace_baseline_id,workspace_overlay_id,workspace_relpath FROM worker_jobs WHERE id=?`, jobID).Scan(&baselineID, &overlayID, &branchRel))
	if filepath.IsAbs(baselineID) || filepath.IsAbs(overlayID.String) || !filepath.IsLocal(branchRel) {
		t.Fatal("worker persisted installation paths")
	}
	queue := NewSQLQueue(restored, 4)
	queue.SetBaselineStore(sourceledger.New(restored, filepath.Join(newRoot, "source-content")).Baselines)
	manager := workspace.NewManager(filepath.Join(newRoot, "worker-branches"), t.TempDir())
	queue.SetWorkerWorkspaceManager(manager)
	return queue, manager
}

func TestUnsealedWorkerEditsSurviveRelocatedArchive(t *testing.T) {
	ctx := t.Context()
	project := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(project, "main.go"), []byte("package before\n"), 0o600))
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", project)
	dataDir := testbaseline.DataDir(t, database)
	queue := NewSQLQueue(database, 1)
	queue.SetBaselineStore(sourceledger.New(database, filepath.Join(dataDir, "source-content")).Baselines)
	queue.SetWorkerWorkspaceManager(workspace.NewManager(filepath.Join(dataDir, "worker-branches"), t.TempDir()))
	id, err := queue.Enqueue(ctx, api.WorkerTask{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: project, WorkspaceRootID: "root-id", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}}, AgentType: "implementer", Prompt: "edit", Brief: "edit"})
	testutil.FailErr(t, "enqueue unsealed worker", err)
	_, err = queue.ClaimNext(ctx, ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim unsealed worker", err)
	task, err := queue.ClaimWorkerBranch(ctx, id)
	testutil.FailErr(t, "materialize unsealed worker", err)
	testutil.FailErr(t, "write unsettled branch edit", os.WriteFile(filepath.Join(task.WorkspaceRoot, "main.go"), []byte("package unfinished\n"), 0o600))
	restored, _ := relocateWorkerInstallation(t, database, id)
	task, ok := restored.Get(id)
	if !ok || task.WorkspaceOverlayPath != "" {
		t.Fatal("unsealed worker state changed")
	}
	body, err := os.ReadFile(filepath.Join(task.WorkspaceRoot, "main.go"))
	testutil.FailErr(t, "read restored unsealed edit", err)
	if string(body) != "package unfinished\n" {
		t.Fatalf("unsealed edit=%q", body)
	}
	reader, err := workspacebaseline.Open(ctx, task.WorkspaceBaselinePath, workspacebaseline.ContentStore(task.WorkspaceBaselinePath))
	testutil.FailErr(t, "open restored baseline", err)
	defer func() { _ = reader.Close() }()
	original, exists, err := reader.Content(ctx, "main.go")
	testutil.FailErr(t, "read original baseline", err)
	if !exists || original != "package before\n" {
		t.Fatalf("restored baseline=%q exists=%v", original, exists)
	}
	testutil.FailErr(t, "remove unsealed tree fixture", os.RemoveAll(task.WorkspaceRoot))
	restoredRoot := testbaseline.DataDir(t, restored.store.db)
	if err := backup.ValidateLiveReferences(ctx, restored.store.db, restoredRoot); err == nil {
		t.Fatal("upgrade readiness accepted missing unsealed edits")
	}
}
