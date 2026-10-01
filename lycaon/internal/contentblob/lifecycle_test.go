package contentblob_test

import (
	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPublicationLeaseProtectsReusedDigestAcrossRootAliases(t *testing.T) {
	database, root := openGCTestDB(t)
	alias := filepath.Join(t.TempDir(), "alias")
	testutil.FailErr(t, "link data root", os.Symlink(root, alias))
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertSession(t, database, "session", projectID)
	store := contentblob.StoreFor(root, projectID)
	sha, size, stored, err := contentblob.Write(store, []byte("reused historical body"))
	testutil.FailErr(t, "write body", err)
	q := db.New(database)
	testutil.FailErr(t, "record body", q.UpsertContentBlobObject(t.Context(), db.UpsertContentBlobObjectParams{ProjectID: projectID, Sha256: sha, ByteSize: size, StoredSize: stored, CreatedAt: db.FormatTime(time.Now())}))
	_, err = database.ExecContext(t.Context(), `INSERT OR IGNORE INTO content_blob_reclaim_queue(project_id,sha256) VALUES(?,?)`, projectID, sha)
	testutil.FailErr(t, "queue old body", err)
	release := bloblifecycle.AcquirePublication(alias)
	deps := gcDeps(t, database, root)
	count, err := contentblob.RunGCBatch(t.Context(), deps)
	if err != nil || count != 0 {
		release()
		t.Fatalf("GC entered an in-flight publication: count=%d err=%v", count, err)
	}
	insertEvidenceRecord(t, database, "session", projectID, "read#1", sha)
	release()
	_, err = contentblob.RunGCBatch(t.Context(), deps)
	testutil.FailErr(t, "run GC after reference commit", err)
	rel, err := contentblob.RelPath(sha)
	testutil.FailErr(t, "body path", err)
	_, err = os.Stat(filepath.Join(store.Root, rel))
	testutil.FailErr(t, "referenced body remains", err)
}

func TestFailedUnlinkPreservesReclaimReceipt(t *testing.T) {
	database, root := openGCTestDB(t)
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertProject(t, database, projectID)
	store := contentblob.StoreFor(root, projectID)
	sha, size, stored, err := contentblob.Write(store, []byte("queued body"))
	testutil.FailErr(t, "write body", err)
	q := db.New(database)
	testutil.FailErr(t, "record body", q.UpsertContentBlobObject(t.Context(), db.UpsertContentBlobObjectParams{ProjectID: projectID, Sha256: sha, ByteSize: size, StoredSize: stored, CreatedAt: db.FormatTime(time.Now())}))
	_, err = database.ExecContext(t.Context(), `INSERT OR IGNORE INTO content_blob_reclaim_queue(project_id,sha256) VALUES(?,?)`, projectID, sha)
	testutil.FailErr(t, "queue body", err)
	rel, err := contentblob.RelPath(sha)
	testutil.FailErr(t, "body path", err)
	abs := filepath.Join(store.Root, rel)
	testutil.FailErr(t, "replace body for deterministic unlink fault", os.Remove(abs))
	testutil.FailErr(t, "create nonempty directory", os.MkdirAll(filepath.Join(abs, "child"), 0o700))
	deps := gcDeps(t, database, root)
	if _, err := contentblob.RunGCBatch(t.Context(), deps); err == nil {
		t.Fatal("expected unlink failure")
	}
	var count int
	testutil.FailErr(t, "count retained reclaim receipt", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM content_blob_reclaim_queue WHERE project_id=? AND sha256=?`, projectID, sha).Scan(&count))
	if count != 1 {
		t.Fatal("failed unlink lost its retry receipt")
	}
	testutil.FailErr(t, "remove fixture fault", os.RemoveAll(abs))
	_, err = contentblob.RunGCBatch(t.Context(), deps)
	testutil.FailErr(t, "retry missing-file cleanup", err)
}
