package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// lostBodyFixture stores one read record and returns its body file path.
func lostBodyFixture(t *testing.T) (store *SQL, sessionID, bodyPath string) {
	t.Helper()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store = NewSQL(sqlDB)
	store.SetDataDir(dir)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	testutil.FailErr(t, "UpsertEvidenceRecord", store.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "read#1", Kind: "read", Shape: "file_region", Fidelity: "structured", SourceTool: "read",
		Path: "src/foo.go", LineRanges: []evidence.LineRange{{Start: 42, End: 42}}, Body: []string{"line 42: token check"},
	}))
	var sha string
	testutil.FailErr(t, "read body digest", sqlDB.QueryRowContext(ctx,
		`SELECT content_blob_sha256 FROM evidence_records WHERE session_id = ? AND handle = 'read#1'`, sess.ID).Scan(&sha))
	rel, err := contentblob.RelPath(sha)
	testutil.FailErr(t, "body path", err)
	return store, sess.ID, filepath.Join(contentblob.StoreFor(dir, testdbseed.DefaultProjectID).Root, filepath.FromSlash(rel))
}

// A missing body file leaves the record loadable without its body.
func TestLoadLedgerKeepsARecordWhoseBodyFileIsMissing(t *testing.T) {
	store, sessionID, bodyPath := lostBodyFixture(t)
	testutil.FailErr(t, "remove body file", os.Remove(bodyPath))

	ev, err := store.LoadLedger(context.Background(), sessionID)
	testutil.FailErr(t, "LoadLedger with a lost body", err)
	got, ok := evidence.ResolveHandle(ev, "read#1")
	if !ok {
		t.Fatal("the record vanished with its body")
	}
	if got.Path != "src/foo.go" || len(got.LineRanges) != 1 || got.LineRanges[0].Start != 42 {
		t.Fatalf("record metadata was not kept: %+v", got)
	}
	if len(got.Body) != 0 {
		t.Fatalf("a lost body loaded as %q", got.Body)
	}
	if evidence.ExcerptMatchesHandle(ev, "read#1", "src/foo.go", 42, "token check") {
		t.Fatal("an excerpt matched a body that no longer exists")
	}
}

func TestLoadLedgerReportsALostBodyOnce(t *testing.T) {
	store, sessionID, bodyPath := lostBodyFixture(t)
	testutil.FailErr(t, "remove body file", os.Remove(bodyPath))

	for range 3 {
		_, err := store.LoadLedger(context.Background(), sessionID)
		testutil.FailErr(t, "LoadLedger", err)
	}
	if got := store.lostBodies.Len(); got != 1 {
		t.Fatalf("lost bodies remembered = %d, want the one digest", got)
	}
}

// A body that cannot be decoded still fails the load.
func TestLoadLedgerStillFailsOnADamagedBody(t *testing.T) {
	store, sessionID, bodyPath := lostBodyFixture(t)
	testutil.FailErr(t, "damage body file", os.WriteFile(bodyPath, []byte("not zstd"), 0o600))

	if _, err := store.LoadLedger(context.Background(), sessionID); err == nil {
		t.Fatal("a damaged body loaded as if it were fine")
	}
}
