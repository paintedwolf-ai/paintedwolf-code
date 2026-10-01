package contentblob_test

import (
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func openGCTestDB(t *testing.T) (*db.Store, string) {
	t.Helper()
	dataDir := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dataDir, "store.db"))
	return database, dataDir
}

func gcDeps(t *testing.T, database *db.Store, dataDir string) contentblob.GCDeps {
	t.Helper()
	return contentblob.GCDeps{
		Database: database, Queries: db.New(database), DataDir: dataDir,
		Guard: testdbfixture.ClaimStore(t, filepath.Join(dataDir, "store.db")),
	}
}

func insertEvidenceRecord(t *testing.T, database *db.Store, sessionID, projectID, handle, sha string) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
		INSERT INTO evidence_records (session_id, project_id, handle, ordinal, kind, content_blob_sha256)
		VALUES (?, ?, ?, 1, 'read', ?)
	`, sessionID, projectID, handle, sha)
	testutil.FailErr(t, "insert evidence record", err)
}

func TestRunGCBatchReclaimsUnreferencedBlob(t *testing.T) {
	database, dataDir := openGCTestDB(t)
	projectID := testdbseed.DefaultProjectID
	sessionID := "session-gc-1"
	testdbseed.InsertSession(t, database, sessionID, projectID)

	store := contentblob.StoreFor(dataDir, projectID)
	sha, byteSize, storedSize, err := contentblob.Write(store, []byte("body that will become unreferenced"))
	testutil.FailErr(t, "write blob", err)

	q := db.New(database)
	ctx := t.Context()
	testutil.FailErr(t, "upsert content blob object", q.UpsertContentBlobObject(ctx, db.UpsertContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha, ByteSize: byteSize, StoredSize: storedSize,
		CreatedAt: db.FormatTime(time.Now().UTC()),
	}))
	insertEvidenceRecord(t, database, sessionID, projectID, "read#1", sha)

	// Deleting the only referencing row fires the reclaim-queue trigger.
	_, err = database.ExecContext(ctx, `DELETE FROM evidence_records WHERE session_id = ? AND handle = ?`, sessionID, "read#1")
	testutil.FailErr(t, "delete evidence record", err)

	var queued int
	testutil.FailErr(t, "count queue", database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM content_blob_reclaim_queue WHERE project_id = ? AND sha256 = ?`, projectID, sha).Scan(&queued))
	if queued != 1 {
		t.Fatalf("expected the delete trigger to queue the orphaned blob, got %d rows", queued)
	}

	rel, err := contentblob.RelPath(sha)
	testutil.FailErr(t, "rel path", err)
	abs := filepath.Join(store.Root, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("blob file must exist before GC: %v", err)
	}

	processed, err := contentblob.RunGCBatch(ctx, gcDeps(t, database, dataDir))
	testutil.FailErr(t, "run gc batch", err)
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}

	var objectCount, queueCount int
	testutil.FailErr(t, "count object", database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM content_blob_objects WHERE project_id = ? AND sha256 = ?`, projectID, sha).Scan(&objectCount))
	testutil.FailErr(t, "count queue after gc", database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM content_blob_reclaim_queue WHERE project_id = ? AND sha256 = ?`, projectID, sha).Scan(&queueCount))
	if objectCount != 0 || queueCount != 0 {
		t.Fatalf("expected both rows gone, got object=%d queue=%d", objectCount, queueCount)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("expected the physical blob to be removed, stat err = %v", err)
	}
}

func TestRunGCBatchSkipsStillReferencedBlob(t *testing.T) {
	database, dataDir := openGCTestDB(t)
	projectID := testdbseed.DefaultProjectID
	sessionID := "session-gc-2"
	testdbseed.InsertSession(t, database, sessionID, projectID)

	store := contentblob.StoreFor(dataDir, projectID)
	sha, byteSize, storedSize, err := contentblob.Write(store, []byte("body that stays referenced"))
	testutil.FailErr(t, "write blob", err)

	q := db.New(database)
	ctx := t.Context()
	testutil.FailErr(t, "upsert content blob object", q.UpsertContentBlobObject(ctx, db.UpsertContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha, ByteSize: byteSize, StoredSize: storedSize,
		CreatedAt: db.FormatTime(time.Now().UTC()),
	}))
	insertEvidenceRecord(t, database, sessionID, projectID, "read#1", sha)

	// Simulate a race: something queued this digest for reclaim (e.g. another
	// row referencing it was deleted) while this row still references it.
	_, err = database.ExecContext(ctx, `INSERT INTO content_blob_reclaim_queue (project_id, sha256) VALUES (?, ?)`, projectID, sha)
	testutil.FailErr(t, "seed reclaim queue", err)

	processed, err := contentblob.RunGCBatch(ctx, gcDeps(t, database, dataDir))
	testutil.FailErr(t, "run gc batch", err)
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}

	var objectCount, queueCount int
	testutil.FailErr(t, "count object", database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM content_blob_objects WHERE project_id = ? AND sha256 = ?`, projectID, sha).Scan(&objectCount))
	testutil.FailErr(t, "count queue after gc", database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM content_blob_reclaim_queue WHERE project_id = ? AND sha256 = ?`, projectID, sha).Scan(&queueCount))
	if objectCount != 1 {
		t.Fatalf("expected the referenced object row to survive, got %d", objectCount)
	}
	if queueCount != 0 {
		t.Fatalf("expected the queue row to be cleared, got %d", queueCount)
	}

	got, err := contentblob.Read(store, sha)
	testutil.FailErr(t, "read surviving blob", err)
	if string(got) != "body that stays referenced" {
		t.Fatalf("surviving blob content changed: %q", got)
	}
}
