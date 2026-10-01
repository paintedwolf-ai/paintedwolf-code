package contentblob_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// lostGuard is a claim whose store path was replaced.
type lostGuard struct{}

func (lostGuard) Verify() error {
	return &hostlock.ClaimLostError{StorePath: "store.db", Reason: "store file was replaced"}
}

// A lost claim stops reclamation: the rows describe another store.
func TestRunGCBatchRefusesWhenTheStoreClaimIsLost(t *testing.T) {
	database, dataDir := openGCTestDB(t)
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertSession(t, database, "session-gc-lost", projectID)
	store := contentblob.StoreFor(dataDir, projectID)
	sha, byteSize, storedSize, err := contentblob.Write(store, []byte("queued but the store is not ours"))
	testutil.FailErr(t, "write blob", err)
	q := db.New(database)
	testutil.FailErr(t, "upsert object", q.UpsertContentBlobObject(t.Context(), db.UpsertContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha, ByteSize: byteSize, StoredSize: storedSize, CreatedAt: db.FormatTime(time.Now().UTC()),
	}))
	_, err = database.ExecContext(t.Context(), `INSERT INTO content_blob_reclaim_queue(project_id,sha256) VALUES(?,?)`, projectID, sha)
	testutil.FailErr(t, "queue blob", err)

	_, err = contentblob.RunGCBatch(t.Context(), contentblob.GCDeps{Database: database, Queries: q, DataDir: dataDir, Guard: lostGuard{}})
	if !errors.Is(err, hostlock.ErrStoreClaimLost) {
		t.Fatalf("GC should refuse on a lost claim, got %v", err)
	}
	rel, err := contentblob.RelPath(sha)
	testutil.FailErr(t, "rel path", err)
	if _, err := os.Stat(filepath.Join(store.Root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("a refused pass deleted the blob: %v", err)
	}
	var objects int
	testutil.FailErr(t, "count objects", database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM content_blob_objects WHERE project_id = ? AND sha256 = ?`, projectID, sha).Scan(&objects))
	if objects != 1 {
		t.Fatalf("a refused pass deleted the object row: %d", objects)
	}
}
