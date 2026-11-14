package backup_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testbackup"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

const placedKey = "0123456789abcdef"

type placedJob struct {
	database db.Handle
	root     string
	folder   string
	tree     string
	opts     backup.CreateOpts
}

// newPlacedJob seeds an unsealed job whose tree lives in a registered folder
// outside the state folder, with the record the manager leaves under its address.
func newPlacedJob(t *testing.T) placedJob {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	root := testbaseline.DataDir(t, database)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs(id,project_id,agent_type,prompt,brief,created_at) VALUES('job',?,'implementer','work','work','2026-01-01T00:00:00Z')`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert worker", err)
	manifest := testbaseline.Durable(t, database, "job", t.TempDir())
	id, err := workspacebaseline.ID(manifest)
	testutil.FailErr(t, "resolve baseline", err)
	_, err = database.ExecContext(t.Context(), `UPDATE worker_jobs SET workspace_baseline_id=?,workspace_relpath=? WHERE id='job'`, id, "worker-branches/"+placedKey+"/job")
	testutil.FailErr(t, "publish workspace", err)

	folder := filepath.Join(t.TempDir(), enginepaths.WorkerPlacementDirName(1, root))
	tree := filepath.Join(enginepaths.WorkerBranchesRootUnder(folder), placedKey, "job")
	testutil.FailErr(t, "create tree", os.MkdirAll(filepath.Join(tree, "pkg"), 0o700))
	testutil.FailErr(t, "write edit", os.WriteFile(filepath.Join(tree, "pkg", "edit.go"), []byte("unfinished"), 0o600))
	testutil.FailErr(t, "write link", os.Symlink("edit.go", filepath.Join(tree, "pkg", "alias.go")))
	meta := workspace.JobMeta{SnapshotComplete: true, Roots: []workspace.JobMetaRoot{{ID: "primary", Label: "primary", Path: "/project", IsPrimary: true}}}
	testutil.FailErr(t, "write layout", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(tree), meta))
	meta.SnapshotComplete = false
	logical := filepath.Join(enginepaths.WorkerBranchesRootUnder(root), placedKey, "job")
	testutil.FailErr(t, "write logical layout", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(logical), meta))

	marker, err := json.Marshal(map[string]any{"format_version": 1, "id": "here", "state_key": enginepaths.ProjectKey(root)})
	testutil.FailErr(t, "encode marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(folder, "placement.json"), marker, 0o600))
	registry, err := json.Marshal(map[string]any{
		"format_version": 1,
		"folders":        []map[string]any{{"id": "here", "path": folder, "volume_root": filepath.Dir(folder), "kind": "volume_root"}},
		"branches":       map[string]string{placedKey + "/job": "here"},
	})
	testutil.FailErr(t, "encode registry", err)
	testutil.FailErr(t, "write registry", os.WriteFile(filepath.Join(root, enginepaths.WorkerPlacementsFileName), registry, 0o600))
	return placedJob{
		database: database, root: root, folder: folder, tree: tree,
		opts: backup.CreateOpts{ConfigDir: root, DBPath: filepath.Join(root, "store.db"), SQLDB: database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion},
	}
}

func TestBackupCarriesAPlacedUnsealedTreeToItsLogicalAddress(t *testing.T) {
	job := newPlacedJob(t)
	archive := filepath.Join(t.TempDir(), "placed.zip")
	manifest, err := backup.Create(t.Context(), job.opts, archive)
	testutil.FailErr(t, "create", err)
	if len(manifest.RetainedBranchTrees) != 1 || manifest.RetainedBranchTrees[0] != "worker-branches/"+placedKey+"/job" {
		t.Fatalf("retained trees = %v", manifest.RetainedBranchTrees)
	}
	restored := t.TempDir()
	_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: restored, ArchivePath: archive, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage", err)
	testutil.FailErr(t, "apply", backup.ApplyPending(t.Context(), restored))
	tree := filepath.Join(enginepaths.WorkerBranchesRootUnder(restored), placedKey, "job")
	raw, err := os.ReadFile(filepath.Join(tree, "pkg", "edit.go"))
	testutil.FailErr(t, "read restored edit", err)
	if string(raw) != "unfinished" {
		t.Fatalf("restored edit = %q", raw)
	}
	if target, err := os.Readlink(filepath.Join(tree, "pkg", "alias.go")); err != nil || target != "edit.go" {
		t.Fatalf("restored link = %q, %v", target, err)
	}
	if !workspace.BranchTreePresent(tree) {
		t.Fatal("the real layout record did not travel with the tree")
	}
	if _, err := os.Stat(filepath.Join(restored, enginepaths.WorkerPlacementsFileName)); !os.IsNotExist(err) {
		t.Fatalf("this device's registry travelled: %v", err)
	}
}

func TestAbsentVolumeFailsBackupButNotUpgradeReadiness(t *testing.T) {
	job := newPlacedJob(t)
	testutil.FailErr(t, "lose the volume", os.Rename(job.folder, job.folder+"-away"))
	_, err := backup.Create(t.Context(), job.opts, filepath.Join(t.TempDir(), "absent.zip"))
	if !errors.Is(err, backup.ErrCaptureIncomplete) {
		t.Fatalf("create = %v", err)
	}
	testutil.FailErr(t, "upgrade readiness", backup.ValidateLiveReferences(t.Context(), job.database, job.root))
}

func TestRecoveryPointLeavesAPlacedTreeWhereItIs(t *testing.T) {
	job := newPlacedJob(t)
	testbackup.Recovery(t, job.database, filepath.Join(job.root, "store.db"))
	_, err := backup.StageLatestUpgradeRecovery(t.Context(), backup.StageOpts{ConfigDir: job.root, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage recovery point", err)
	if _, err := os.Stat(filepath.Join(job.tree, "pkg", "edit.go")); err != nil {
		t.Fatalf("placed tree changed: %v", err)
	}
}
