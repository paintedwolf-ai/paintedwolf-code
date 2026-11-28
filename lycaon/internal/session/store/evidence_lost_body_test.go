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
func lostBodyFixture(t *testing.T) (store *Repositories, sessionID, bodyPath string) {
	t.Helper()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store = NewSQL(sqlDB)
	store.SetDataDir(dir)
	ctx := context.Background()
	sess, err := store.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	testutil.FailErr(t, "UpsertEvidenceRecord", store.Evidence.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
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

	ev, err := store.Evidence.LoadLedger(context.Background(), sessionID)
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
		_, err := store.Evidence.LoadLedger(context.Background(), sessionID)
		testutil.FailErr(t, "LoadLedger", err)
	}
	if got := store.core.lostBodies.Len(); got != 1 {
		t.Fatalf("lost bodies remembered = %d, want the one digest", got)
	}
}

// A damaged body loads with BodyUnavailable=true so the session stays recoverable,
// other records remain usable, the damaged blob is untouched, and the damaged
// record can never ground any citation.
func TestLoadLedgerMarksDamagedBodyUnavailableAndRecovers(t *testing.T) {
	store, sessionID, bodyPath := lostBodyFixture(t)
	damagedBytes := []byte("not zstd")
	testutil.FailErr(t, "damage body file", os.WriteFile(bodyPath, damagedBytes, 0o600))

	// Add a second healthy observation on the same session to confirm recovery.
	ctx := context.Background()
	testutil.FailErr(t, "UpsertEvidenceRecord healthy", store.Evidence.UpsertEvidenceRecord(ctx, sessionID, evidence.Record{
		Handle: "read#2", Kind: "read", Shape: "file_region", Fidelity: "structured", SourceTool: "read",
		Path: "src/bar.go", LineRanges: []evidence.LineRange{{Start: 1, End: 2}}, Body: []string{"1| package bar", "2| const OK = true"},
	}))

	ev, err := store.Evidence.LoadLedger(ctx, sessionID)
	testutil.FailErr(t, "LoadLedger with a damaged body", err)

	got, ok := evidence.ResolveHandle(ev, "read#1")
	if !ok {
		t.Fatal("damaged record vanished from Handles (would violate non-empty ledger / monotonic ordinals)")
	}
	if !ev.BodyUnavailable("read#1") || len(got.Body) != 0 {
		t.Fatalf("damaged record = %+v, want ev.BodyUnavailable(read#1)=true and empty Body", got)
	}
	if handles := ev.UnavailableBodyHandles(); len(handles) != 1 || handles[0] != "read#1" {
		t.Fatalf("UnavailableBodyHandles = %v, want [read#1]", handles)
	}

	// Damaged record must never ground via handle, path, coordinate, or excerpt.
	if res := evidence.ResolveObservation(evidence.CitationRoots{}, ev, "read#1", evidence.Triple{Path: "src/foo.go", Line: 42}); res.Verdict != evidence.VerdictUnverifiable || res.Reason != evidence.VerifyReasonBodyUnavailable {
		t.Fatalf("ResolveObservation(damaged) = %+v, want unverifiable/%s", res, evidence.VerifyReasonBodyUnavailable)
	}
	if res := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{Path: "src/foo.go", Line: 42}, ev, ""); res.Verdict != evidence.VerdictUnverifiable {
		t.Fatalf("Resolve(damaged path) = %+v, want unverifiable", res)
	}
	if evidence.ExcerptMatchesHandle(ev, "read#1", "src/foo.go", 42, "token check") {
		t.Fatal("damaged record matched excerpt")
	}

	// Healthy record in the same ledger remains fully usable.
	if res := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{Path: "src/bar.go", Line: 2, Excerpt: "const OK = true"}, ev, ""); res.Verdict != evidence.VerdictMatched || res.Handle != "read#2" {
		t.Fatalf("Resolve(healthy) = %+v, want matched/read#2", res)
	}

	// Damaged blob must never be deleted or rewritten.
	raw, err := os.ReadFile(bodyPath)
	testutil.FailErr(t, "read damaged blob after load", err)
	if string(raw) != string(damagedBytes) {
		t.Fatalf("damaged blob mutated: got %q want %q", raw, damagedBytes)
	}
}
