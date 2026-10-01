package sourceledger

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func TestWorkerBaselineSurvivesBranchRemovalAndBlobSweep(t *testing.T) {
	store, ctx := openLedger(t)
	_, err := store.sqlDB.ExecContext(ctx, `INSERT INTO worker_jobs(id,project_id,agent_type,prompt,brief,created_at) VALUES('baseline-job','p1','implementer','work','work','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "insert worker", err)
	branch := t.TempDir()
	body := "original branch content\n"
	testutil.FailErr(t, "write baseline source", os.WriteFile(filepath.Join(branch, "a.txt"), []byte(body), 0o600))
	ref, err := store.baselines.Capture(ctx, "baseline-job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture worker baseline", err)
	_, err = store.sqlDB.ExecContext(ctx, "UPDATE worker_jobs SET workspace_baseline_id=? WHERE id='baseline-job'", strings.TrimSuffix(filepath.Base(ref), ".db"))
	testutil.FailErr(t, "attach baseline", err)
	testutil.FailErr(t, "remove source branch", os.RemoveAll(branch))
	testutil.FailErr(t, "sweep referenced blobs", store.SweepBlobs(ctx))
	reader, err := workspacebaseline.Open(ctx, ref, store.objects)
	testutil.FailErr(t, "reopen baseline", err)
	content, exists, err := reader.Content(ctx, "a.txt")
	testutil.FailErr(t, "read retained merge base", err)
	if !exists || content != body {
		t.Fatalf("retained baseline: exists=%v content=%q", exists, content)
	}
	defer func() { _ = reader.Close() }()
	releaseArchive := sync.OnceFunc(store.AcquireRetentionLease())
	defer releaseArchive()
	_, err = store.sqlDB.ExecContext(ctx, "DELETE FROM worker_jobs WHERE id='baseline-job'")
	testutil.FailErr(t, "delete owning worker", err)
	if err := store.SweepBlobs(ctx); !errors.Is(err, ErrBlobMaintenanceDeferred) {
		releaseArchive()
		t.Fatalf("archive retention lease did not defer collection: %v", err)
	}
	content, exists, err = reader.Content(ctx, "a.txt")
	releaseArchive()
	testutil.FailErr(t, "read baseline retained for archive", err)
	if !exists || content != body {
		t.Fatalf("archive baseline: exists=%v content=%q", exists, content)
	}
	testutil.FailErr(t, "sweep released blobs", store.SweepBlobs(ctx))
	rel, err := sourceblob.RelPath(sourceblob.ContentSHA([]byte(body)))
	testutil.FailErr(t, "object path", err)
	if _, err := os.Stat(filepath.Join(store.objects.Root(), rel)); !os.IsNotExist(err) {
		t.Fatalf("unreferenced object remains: %v", err)
	}
}
