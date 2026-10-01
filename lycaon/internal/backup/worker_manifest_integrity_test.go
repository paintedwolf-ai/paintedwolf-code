package backup_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func TestArchiveRejectsChangedSealedWorkerManifest(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	root := testbaseline.DataDir(t, database)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs(id,project_id,agent_type,prompt,brief,created_at) VALUES('job',?,'implementer','work','work','2026-01-01T00:00:00Z')`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert worker", err)
	manifest := testbaseline.Durable(t, database, "job", t.TempDir())
	id, err := workspacebaseline.ID(manifest)
	testutil.FailErr(t, "resolve manifest identity", err)
	_, err = database.ExecContext(t.Context(), `UPDATE worker_jobs SET workspace_baseline_id=? WHERE id='job'`, id)
	testutil.FailErr(t, "publish baseline", err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO worker_baselines(id,job_id,created_at) VALUES('00000000-0000-4000-8000-000000000001','job','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "leave interrupted unpublished capture", err)
	opts := backup.CreateOpts{ConfigDir: root, DBPath: filepath.Join(root, "store.db"), SQLDB: database, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}
	_, err = backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "valid.zip"))
	testutil.FailErr(t, "capture intact manifest", err)
	testutil.FailErr(t, "make fixture writable", os.Chmod(manifest, 0o600))
	file, err := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0)
	testutil.FailErr(t, "open fixture for corruption", err)
	_, err = file.WriteString("changed sealed bytes")
	testutil.FailErr(t, "change sealed manifest", err)
	testutil.FailErr(t, "close changed manifest", file.Close())
	dest := filepath.Join(t.TempDir(), "invalid.zip")
	if _, err := backup.Create(t.Context(), opts, dest); err == nil {
		t.Fatal("archive accepted changed sealed manifest")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("failed capture published archive: %v", err)
	}
}
