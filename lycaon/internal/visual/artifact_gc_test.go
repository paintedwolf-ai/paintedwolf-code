package visual

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGroupCleanupRetriesAfterArchiveCaptureAndRestart(t *testing.T) {
	f := newDurableFixture(t, []string{"session"})
	var ids []string
	for range 2 {
		wire, err := f.store.Put(t.Context(), "session", Entry{Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: onePixelPNG(t)})
		testutil.FailErr(t, "publish group member", err)
		ids = append(ids, wire.ID)
	}
	rec := f.record(t, ids[0])
	release := bloblifecycle.AcquirePublication(f.dataDir)
	count, err := f.store.DeleteGroup(t.Context(), f.project, ids, "reviewed retention")
	if err != nil || count != 2 {
		release()
		t.Fatalf("delete group count=%d err=%v", count, err)
	}
	err = f.store.CollectGarbage(t.Context())
	if err != nil {
		release()
		testutil.FailErr(t, "defer archive-owned cleanup", err)
	}
	usage, usageErr := f.store.StorageUsage(t.Context(), f.project)
	if usageErr != nil {
		release()
		testutil.FailErr(t, "measure pending project storage", usageErr)
	}
	if usage.UsedBytes != rec.StoredSize {
		release()
		t.Fatalf("pending body vanished from project accounting: %+v", usage)
	}
	retained := f.blobExists(t, rec)
	var queued int
	err = f.sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM artifact_gc_queue`).Scan(&queued)
	release()
	testutil.FailErr(t, "count durable cleanup receipts", err)
	if !retained || queued != 1 {
		t.Fatalf("capture bytes=%v queued=%d", retained, queued)
	}
	testutil.FailErr(t, "retry after process restart", f.restart().CollectGarbage(t.Context()))
	if f.blobExists(t, rec) {
		t.Fatal("completed capture kept unreferenced body")
	}
	testutil.FailErr(t, "count completed receipts", f.sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM artifact_gc_queue`).Scan(&queued))
	if queued != 0 {
		t.Fatal("completed cleanup receipt remains")
	}
}

func TestArtifactGarbageCollectionKeepsRepublishedBody(t *testing.T) {
	f := newDurableFixture(t, []string{"session"})
	entry := Entry{Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: onePixelPNG(t)}
	first, err := f.store.Put(t.Context(), "session", entry)
	testutil.FailErr(t, "publish first owner", err)
	release := bloblifecycle.AcquirePublication(f.dataDir)
	_, err = f.store.DeleteGroup(t.Context(), f.project, []string{first.ID}, "retention")
	if err != nil {
		release()
		testutil.FailErr(t, "tombstone first owner", err)
	}
	next, err := f.store.Put(t.Context(), "session", entry)
	release()
	testutil.FailErr(t, "publish new owner before deferred cleanup", err)
	testutil.FailErr(t, "recheck live reference during cleanup", f.store.CollectGarbage(t.Context()))
	rec := f.record(t, next.ID)
	if !f.blobExists(t, rec) {
		t.Fatal("cleanup deleted republished content")
	}
}

func TestArtifactCleanupFailureKeepsReceipt(t *testing.T) {
	f := newDurableFixture(t, []string{"session"})
	wire, err := f.store.Put(t.Context(), "session", Entry{Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: onePixelPNG(t)})
	testutil.FailErr(t, "publish body", err)
	rec := f.record(t, wire.ID)
	release := bloblifecycle.AcquirePublication(f.dataDir)
	_, err = f.store.DeleteGroup(t.Context(), f.project, []string{wire.ID}, "retention")
	release()
	testutil.FailErr(t, "commit pending deletion", err)
	path := filepath.Join(f.blobDir(t), rec.ContentHash)
	testutil.FailErr(t, "remove disposable fixture body", os.Remove(path))
	testutil.FailErr(t, "create blocked cleanup path", os.Mkdir(path, 0o700))
	testutil.FailErr(t, "keep cleanup path nonempty", os.Mkdir(filepath.Join(path, "child"), 0o700))
	if err := f.store.CollectGarbage(t.Context()); err == nil {
		t.Fatal("invalid body path accepted")
	}
	var queued int
	testutil.FailErr(t, "count failed receipt", f.sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM artifact_gc_queue`).Scan(&queued))
	if queued != 1 {
		t.Fatal("failed unlink lost durable receipt")
	}
	testutil.FailErr(t, "remove blocker child", os.Remove(filepath.Join(path, "child")))
	testutil.FailErr(t, "remove blocker", os.Remove(path))
	testutil.FailErr(t, "retry missing body idempotently", f.store.CollectGarbage(t.Context()))
}

func TestArtifactCleanupAdvancesPastFailedAndBusyReceipts(t *testing.T) {
	f := newDurableFixture(t, []string{"session"})
	dir := f.blobDir(t)
	for i := range artifactPruneBatchSize + 1 {
		hash := fmt.Sprintf("%064x", i+1)
		path := filepath.Join(dir, hash)
		if i < artifactPruneBatchSize {
			testutil.FailErr(t, "create blocked receipt path", os.Mkdir(path, 0o700))
			testutil.FailErr(t, "keep blocked path nonempty", os.Mkdir(filepath.Join(path, "child"), 0o700))
		} else {
			testutil.FailErr(t, "create later orphan body", os.WriteFile(path, []byte("orphan"), 0o600))
		}
		_, err := f.sqlDB.ExecContext(t.Context(), `INSERT INTO artifact_gc_queue(project_id,content_hash,stored_size) VALUES(?,?,6)`, f.project, hash)
		testutil.FailErr(t, "record cleanup obligation", err)
	}
	if err := f.store.CollectGarbage(t.Context()); err == nil {
		t.Fatal("failed receipt errors lost")
	}
	testutil.FailErr(t, "collect later valid receipt", f.store.CollectGarbage(t.Context()))
	later := filepath.Join(dir, fmt.Sprintf("%064x", artifactPruneBatchSize+1))
	if _, err := os.Stat(later); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed first batch starved later body: %v", err)
	}
	// Reinsert a later receipt and hold the shared lease over the first full batch.
	_, err := f.sqlDB.ExecContext(t.Context(), `INSERT INTO artifact_gc_queue(project_id,content_hash,stored_size) VALUES(?,?,0)`, f.project, fmt.Sprintf("%064x", artifactPruneBatchSize+1))
	testutil.FailErr(t, "record later retry", err)
	release := bloblifecycle.AcquirePublication(f.dataDir)
	err = f.store.CollectGarbage(t.Context())
	release()
	testutil.FailErr(t, "defer busy first batch", err)
	testutil.FailErr(t, "collect after busy batch", f.store.CollectGarbage(t.Context()))
	var queued int
	testutil.FailErr(t, "count preserved failed receipts", f.sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM artifact_gc_queue`).Scan(&queued))
	if queued != artifactPruneBatchSize {
		t.Fatalf("busy receipts starved progress or failed receipts lost: %d", queued)
	}
}
