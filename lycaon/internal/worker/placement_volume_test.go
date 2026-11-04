//go:build darwin || linux

package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testvolume"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// A claim on a second volume stores only its logical address, survives the
// volume going away and returning, and travels in a backup to another device.
func TestPlacedWorkerSurvivesEjectAndRelocatesThroughBackup(t *testing.T) {
	volume := testvolume.New(t, testvolume.Movable)
	t.Cleanup(workspace.UseCopyAsCloneForTest())
	ctx := t.Context()
	project := volume.Dir(t, "project")
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(project, "main.go"), []byte("package before\n"), 0o600))
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", project)
	dataDir := testbaseline.DataDir(t, database)
	queue := NewSQLQueue(database, 1)
	queue.SetBaselineStore(sourceledger.New(database, filepath.Join(dataDir, "source-content")).Baselines)
	queue.SetWorkerWorkspaceManager(workspace.NewManager(enginepaths.WorkerBranchesRootUnder(dataDir), t.TempDir()))
	id, err := queue.Enqueue(ctx, api.WorkerTask{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: project, WorkspaceRootID: "root-id", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}}, AgentType: "implementer", Prompt: "edit", Brief: "edit"})
	testutil.FailErr(t, "enqueue", err)
	_, err = queue.ClaimNext(ctx, ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim", err)
	task, err := queue.ClaimWorkerBranch(ctx, id)
	testutil.FailErr(t, "claim branch", err)
	root := task.WorkspaceRoot
	if !strings.HasPrefix(root, volume.Root+string(filepath.Separator)) {
		t.Fatalf("branch is not on the source's volume: %s", root)
	}
	testutil.FailErr(t, "worker edit", os.WriteFile(filepath.Join(root, "main.go"), []byte("package unfinished\n"), 0o600))

	var stored string
	testutil.FailErr(t, "read stored address", database.QueryRowContext(ctx, `SELECT workspace_relpath FROM worker_jobs WHERE id=?`, id).Scan(&stored))
	if !filepath.IsLocal(stored) || !strings.HasPrefix(stored, enginepaths.WorkerBranchesDirName+"/") {
		t.Fatalf("the store holds a volume path: %s", stored)
	}
	read, ok := queue.Get(id)
	if !ok || read.WorkspaceRoot != root {
		t.Fatalf("resolved root = %+v", read)
	}

	volume.Eject(t)
	read, ok = queue.Get(id)
	if !ok || read.WorkspaceRoot != root {
		t.Fatalf("resolved root while ejected = %+v", read)
	}
	if _, _, err := queue.EnsureWorkerBranch(ctx, id); !errors.Is(err, ErrWorkerBranchUnavailable) {
		t.Fatalf("ensure while ejected = %v", err)
	}
	testutil.FailErr(t, "upgrade readiness with an absent volume", backup.ValidateLiveReferences(ctx, database, dataDir))
	createOpts := backup.CreateOpts{ConfigDir: dataDir, DBPath: filepath.Join(dataDir, "store.db"), SQLDB: database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
	if _, err := backup.Create(ctx, createOpts, filepath.Join(t.TempDir(), "absent.zip")); !errors.Is(err, backup.ErrCaptureIncomplete) {
		t.Fatalf("backup with an absent unsealed tree = %v", err)
	}

	volume.Remount(t)
	_, lease, err := queue.EnsureWorkerBranch(ctx, id)
	testutil.FailErr(t, "ensure after remount", err)
	body, err := os.ReadFile(filepath.Join(lease.Root, "main.go"))
	lease.Release()
	testutil.FailErr(t, "read after remount", err)
	if string(body) != "package unfinished\n" {
		t.Fatalf("edit after remount = %q", body)
	}

	relocated, _ := relocateWorkerInstallation(t, database, id)
	moved, ok := relocated.Get(id)
	if !ok || strings.HasPrefix(moved.WorkspaceRoot, volume.Root) {
		t.Fatalf("restored root = %+v", moved)
	}
	body, err = os.ReadFile(filepath.Join(moved.WorkspaceRoot, "main.go"))
	testutil.FailErr(t, "read restored edit", err)
	if string(body) != "package unfinished\n" {
		t.Fatalf("restored edit = %q", body)
	}
	if _, err := workspace.LoadBranchLayout(moved.WorkspaceRoot); err != nil {
		t.Fatalf("restored layout: %v", err)
	}
}
