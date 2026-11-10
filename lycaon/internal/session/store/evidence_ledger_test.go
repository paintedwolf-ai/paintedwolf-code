package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvidenceRecordPayloadHasOneVersionedShape(t *testing.T) {
	raw, err := marshalEvidenceLineRanges(evidence.Record{LineRanges: []evidence.LineRange{{Start: 2, End: 3}}})
	testutil.FailErr(t, "marshalEvidenceLineRanges", err)
	var payload map[string]any
	testutil.FailErr(t, "decode payload", json.Unmarshal([]byte(raw), &payload))
	if payload["v"] != float64(1) {
		t.Fatalf("payload = %s, want v=1 object", raw)
	}
	var rec evidence.Record
	if err := unmarshalEvidenceLineRanges(`[{"start":2,"end":3}]`, &rec); err == nil {
		t.Fatal("array payload must be rejected")
	}
}

func TestEvidenceLedgerStore_roundTrip(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	body := []string{"line 42: token check"}
	ranges := []evidence.LineRange{{Start: 42, End: 42}}
	rec := evidence.Record{
		Handle:     "read#1",
		Kind:       "read",
		Shape:      "file_region",
		Fidelity:   "structured",
		SourceTool: "read",
		Path:       "src/foo.go",
		LineRanges: ranges,
		Body:       body,
	}
	testutil.FailErr(t, "UpsertEvidenceRecord", store.UpsertEvidenceRecord(ctx, sess.ID, rec))

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	got, ok := evidence.ResolveHandle(ev, "read#1")
	if !ok {
		t.Fatal("missing read#1")
	}
	if got.Path != rec.Path || len(got.Body) != 1 || got.Body[0] != body[0] {
		t.Fatalf("record = %+v want path/body round-trip", got)
	}
	if len(got.LineRanges) != 1 || got.LineRanges[0].Start != 42 {
		t.Fatalf("line_ranges = %+v", got.LineRanges)
	}
}

func TestEvidenceLedgerStore_upsertIdempotent(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	rec := evidence.Record{Handle: "read#1", Kind: "read", Path: "a.go", Body: []string{"x"}}
	testutil.FailErr(t, "UpsertEvidenceRecord first", store.UpsertEvidenceRecord(ctx, sess.ID, rec))
	testutil.FailErr(t, "UpsertEvidenceRecord retry", store.UpsertEvidenceRecord(ctx, sess.ID, rec))

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if len(ev.Handles) != 1 {
		t.Fatalf("handles = %d want 1", len(ev.Handles))
	}
}

func TestEvidenceLedgerStore_markSuperseded(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	old := evidence.Record{Handle: "read#1", Kind: "read", Path: "src/a.go", Body: []string{"old"}}
	newRec := evidence.Record{Handle: "read#2", Kind: "read", Path: "src/a.go", Body: []string{"new"}}
	testutil.FailErr(t, "UpsertEvidenceRecord old", store.UpsertEvidenceRecord(ctx, sess.ID, old))
	testutil.FailErr(t, "UpsertEvidenceRecord new", store.UpsertEvidenceRecord(ctx, sess.ID, newRec))
	testutil.FailErr(t, "MarkSuperseded", store.MarkSuperseded(ctx, sess.ID, "read#1", "read#2"))

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if rec, ok := evidence.ResolveHandle(ev, "read#1"); !ok || rec.SupersededBy != "read#2" || rec.Body[0] != "old" {
		t.Fatalf("historical observation lost: %+v", rec)
	}
	if got := ev.ByPath["src/a.go"]; len(got) != 1 || got[0] != "read#2" {
		t.Fatalf("current path binding = %v", got)
	}
	got, ok := evidence.ResolveHandle(ev, "read#2")
	if !ok || got.Body[0] != "new" {
		t.Fatalf("live record = %+v ok=%v", got, ok)
	}
}

func TestMemoryStoreCommitEvidenceSequentialOrdinals(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	h1, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "a.go"}, "contents of a.go")
	testutil.FailErr(t, "first commit", err)
	h2, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "b.go"}, "contents of b.go")
	testutil.FailErr(t, "second commit", err)
	if h1 != "read#1" || h2 != "read#2" {
		t.Fatalf("handles = %q, %q want read#1, read#2", h1, h2)
	}
}

func TestMemoryStoreCommitEvidenceOrdinalSurvivesCrossKindSupersession(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	readBody := func(body string) string {
		return `{"path":"a.go","content":"1|` + body + `","offset":1,"end_line":1}`
	}
	first, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "a.go"}, readBody("before"))
	testutil.FailErr(t, "first read", err)
	_, _, err = store.CommitEvidenceToolResult(ctx, sess.ID, dir, "write", map[string]any{"path": "a.go"}, "wrote a.go")
	testutil.FailErr(t, "write", err)
	second, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "a.go"}, readBody("after"))
	testutil.FailErr(t, "second read", err)

	if first != "read#1" || second != "read#2" {
		t.Fatalf("read handles = %q, %q want read#1, read#2", first, second)
	}
	row, ok := store.evidence[sess.ID]["read#1"]
	if !ok || row.supersededBy != "write#1" {
		t.Fatalf("historical read#1 = %+v ok=%v", row, ok)
	}
}

func TestCommitEvidenceToolResult_assignsOrdinalAndPersists(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	readJSON := `{"path":"src/a.go","content":"1|package a","offset":1,"end_line":1}`
	handle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "src/a.go"}, readJSON)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)
	if handle != "read#1" {
		t.Fatalf("handle = %q want read#1", handle)
	}

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if !evidence.PathObserved(ev, "src/a.go") {
		t.Fatalf("paths = %v", evidence.ObservedPathsSorted(ev))
	}
}

func TestCommitEvidenceToolResult_surveyFlagRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	overflowGrep := `{"distribution":[{"count":200,"dir":"."}],"truncated":true,"total":200,"selected":0}`
	handle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "grep", map[string]any{"pattern": "needle"}, overflowGrep)
	testutil.FailErr(t, "CommitEvidenceToolResult overflow grep", err)
	if handle != "grep#1" {
		t.Fatalf("handle = %q want grep#1", handle)
	}

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	got, ok := evidence.ResolveHandle(ev, handle)
	if !ok {
		t.Fatal("missing grep#1")
	}
	if !got.Survey {
		t.Fatalf("survey = false want true for overflow grep parent")
	}

	literalGrep := `{"matches":[{"path":"a.go","line":1,"content":"x"}]}`
	handle2, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "grep", map[string]any{"pattern": "x", "max_matches": 20}, literalGrep)
	testutil.FailErr(t, "CommitEvidenceToolResult literal grep", err)
	if handle2 != "grep#2" {
		t.Fatalf("handle = %q want grep#2", handle2)
	}
	ev, err = store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger after literal", err)
	got2, ok := evidence.ResolveHandle(ev, handle2)
	if !ok {
		t.Fatal("missing grep#2")
	}
	if got2.Survey {
		t.Fatal("survey = true want false for literal grep parent")
	}
}

// TestCommitEvidenceToolResult_webSearchURLsPersist proves every URL a web_search
// result named — not just the first — survives the durable store round-trip and grounds.
func TestCommitEvidenceToolResult_webSearchURLsPersist(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	searchJSON := `{"results":[` +
		`{"url":"https://github.com/cowrie/cowrie"},` +
		`{"url":"https://medium.com/@fa.ouardirhi/cowrie-honeypot-in-action"},` +
		`{"url":"https://docs.cowrie.org/"}` +
		`],"provider":"brave"}`
	_, _, err = store.CommitEvidenceToolResult(ctx, sess.ID, dir, "web_search", map[string]any{"query": "cowrie"}, searchJSON)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	for _, want := range []string{
		"https://github.com/cowrie/cowrie",
		"https://medium.com/@fa.ouardirhi/cowrie-honeypot-in-action",
		"https://docs.cowrie.org/",
	} {
		if !evidence.VerifyURLObserved(ev, want) {
			t.Fatalf("url %q not grounded after reload; observed = %v", want, evidence.ObservedURLsSorted(ev))
		}
	}
}

func TestCommitEvidenceToolResult_reReadSupersedesPriorLiveRow(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	read1 := `{"path":"src/a.go","content":"1|old body","offset":1,"end_line":1}`
	read2 := `{"path":"src/a.go","content":"1|new body","offset":1,"end_line":1}`
	h1, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "src/a.go"}, read1)
	testutil.FailErr(t, "first read commit", err)
	h2, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "src/a.go"}, read2)
	testutil.FailErr(t, "second read commit", err)
	if h1 != "read#1" || h2 != "read#2" {
		t.Fatalf("handles = %q, %q", h1, h2)
	}

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if rec, ok := evidence.ResolveHandle(ev, "read#1"); !ok || rec.SupersededBy != "read#2" {
		t.Fatalf("historical observation lost: %+v", rec)
	}
	got, ok := evidence.ResolveHandle(ev, "read#2")
	if !ok || !strings.Contains(strings.Join(got.Body, "\n"), "new body") {
		t.Fatalf("live record = %+v ok=%v", got, ok)
	}

	var supersededBy sql.NullString
	err = sqlDB.QueryRowContext(ctx, `
		SELECT superseded_by FROM evidence_records WHERE session_id = ? AND handle = ?
	`, sess.ID, "read#1").Scan(&supersededBy)
	testutil.FailErr(t, "query superseded_by", err)
	if !supersededBy.Valid || supersededBy.String != "read#2" {
		t.Fatalf("read#1 superseded_by = %v want read#2", supersededBy)
	}
}

func TestCommitEvidenceToolResult_mutationSupersedesRead(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	readJSON := `{"path":"src/a.go","content":"1|before edit","offset":1,"end_line":1}`
	readHandle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "src/a.go"}, readJSON)
	testutil.FailErr(t, "read commit", err)
	writeHandle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "write", map[string]any{"path": "src/a.go"}, "wrote src/a.go")
	testutil.FailErr(t, "write commit", err)
	if readHandle != "read#1" || writeHandle != "write#1" {
		t.Fatalf("handles = %q, %q", readHandle, writeHandle)
	}

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if rec, ok := evidence.ResolveHandle(ev, "read#1"); !ok || rec.SupersededBy != "write#1" {
		t.Fatalf("historical observation lost: %+v", rec)
	}
	if _, ok := evidence.ResolveHandle(ev, "write#1"); !ok {
		t.Fatal("write#1 must be live mutation evidence for src/a.go")
	}
}

// A later read preserves the write's mutation evidence for finding citations.
func TestCommitEvidenceToolResult_reReadPreservesMutation(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	writeHandle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "write", map[string]any{"path": "game.py"}, "wrote game.py")
	testutil.FailErr(t, "write commit", err)
	readJSON := `{"path":"game.py","content":"1|import math","offset":1,"end_line":1}`
	readHandle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "game.py"}, readJSON)
	testutil.FailErr(t, "read commit", err)
	if writeHandle != "write#1" || readHandle != "read#1" {
		t.Fatalf("handles = %q, %q", writeHandle, readHandle)
	}

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if _, ok := evidence.ResolveHandle(ev, "write#1"); !ok {
		t.Fatal("read of a written path must not supersede the write mutation handle")
	}
	if _, ok := evidence.ResolveHandle(ev, "read#1"); !ok {
		t.Fatal("read#1 should remain live for content citations")
	}
}

// Memory store parity for reReadPreservesMutation.
func TestMemoryStoreCommitEvidence_reReadPreservesMutation(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	_, _, err = store.CommitEvidenceToolResult(ctx, sess.ID, dir, "write", map[string]any{"path": "game.py"}, "wrote game.py")
	testutil.FailErr(t, "write commit", err)
	readJSON := `{"path":"game.py","content":"1|import math","offset":1,"end_line":1}`
	_, _, err = store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "game.py"}, readJSON)
	testutil.FailErr(t, "read commit", err)

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if _, ok := evidence.ResolveHandle(ev, "write#1"); !ok {
		t.Fatal("read of a written path must not supersede the write mutation handle (memory store)")
	}
}

// A directory grep that touches README leaves the earlier full read live, as does
// a later grep that drops README, so citations still ground.
func TestCommitEvidenceToolResult_grepDoesNotRetireReadOnPartialOverlap(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	readJSON := `{"path":"README.md","content":"1|# title\n25\t| Engine | load |\n","offset":1,"end_line":25}`
	readHandle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "README.md"}, readJSON)
	testutil.FailErr(t, "read commit", err)
	if readHandle != "read#1" {
		t.Fatalf("read handle = %q", readHandle)
	}

	grepTouchREADME := `{"matches":[{"path":"README.md","line":25,"content":"| Engine | load |"},{"path":"vocab.py","line":1,"content":"profiles"}]}`
	_, _, err = store.CommitEvidenceToolResult(ctx, sess.ID, dir, "grep", map[string]any{"pattern": "Engine", "path": "."}, grepTouchREADME)
	testutil.FailErr(t, "grep touch README", err)

	grepTestsOnly := `{"matches":[{"path":"tests/a.py","line":1,"content":"def test_x():"}]}`
	_, _, err = store.CommitEvidenceToolResult(ctx, sess.ID, dir, "grep", map[string]any{"pattern": "test_", "path": "tests"}, grepTestsOnly)
	testutil.FailErr(t, "grep tests only", err)

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	got, ok := evidence.ResolveHandle(ev, "read#1")
	if !ok {
		t.Fatal("read#1 must stay live after greps that touched then dropped README")
	}
	if !evidence.PathObserved(ev, "README.md") {
		t.Fatalf("README.md missing from live paths: %v", evidence.ObservedPathsSorted(ev))
	}
	if !strings.Contains(strings.Join(got.Body, "\n"), "| Engine | load |") {
		t.Fatalf("read body lost citeable content: %+v", got.Body)
	}
}

func TestCommitEvidenceToolResult_externalFileCustodyAndModelIngestion(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	// 1. External http_request with response_path records external custody on saved.txt without marking model ingestion.
	_, _, err = store.CommitEvidenceToolResult(
		ctx,
		sess.ID,
		dir,
		"http_request",
		map[string]any{"url": "https://api.external.example/v1/payload.txt", "response_path": "saved.txt"},
		`{"status":200,"url":"https://api.external.example/v1/payload.txt","response_path":"`+filepath.ToSlash(filepath.Join(dir, "saved.txt"))+`"}`,
	)
	testutil.FailErr(t, "http_request response_path", err)

	untrusted, err := store.SessionUntrustedContentResult(ctx, sess.ID)
	testutil.FailErr(t, "SessionUntrustedContentResult after download", err)
	if untrusted {
		t.Fatal("response_path download must not mark model ingestion before saved.txt is read into model context")
	}

	// 2. Subsequent read of saved.txt marks untrusted ingestion and propagates the observed URL.
	readJSON := `{"path":"saved.txt","content":"1|remote payload\n","offset":1,"end_line":1}`
	readHandle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "saved.txt"}, readJSON)
	testutil.FailErr(t, "read saved.txt", err)

	untrusted, err = store.SessionUntrustedContentResult(ctx, sess.ID)
	testutil.FailErr(t, "SessionUntrustedContentResult after read", err)
	if !untrusted {
		t.Fatal("reading external custody file into model context must mark untrusted ingestion")
	}

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	gotRead, ok := evidence.ResolveHandle(ev, readHandle)
	gotURLs := evidence.ObservedURLsForRecord(gotRead)
	if !ok || !gotRead.UntrustedContent || len(gotURLs) != 1 || gotURLs[0] != "https://api.external.example/v1/payload.txt" {
		t.Fatalf("read record = %+v urls=%v, want UntrustedContent=true and propagated ObservedURLs", gotRead, gotURLs)
	}

	// 3. Partial mutation (edit) preserves external custody on a subsequent read, whereas full replacement (write) clears it; verify SQL and Memory parity.
	for _, backend := range []struct {
		name string
		s    interface {
			Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
			CommitEvidenceToolResult(context.Context, string, string, string, map[string]any, string) (string, string, error)
			LoadLedger(context.Context, string) (evidence.Ledger, error)
		}
	}{
		{"sql", store},
		{"memory", NewMemory()},
	} {
		t.Run(backend.name+"_partial_vs_full_replacement", func(t *testing.T) {
			s2, err := backend.s.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "Create", err)
			_, _, err = backend.s.CommitEvidenceToolResult(
				ctx, s2.ID, dir, "http_request",
				map[string]any{"url": "https://api.external.example/v1/item.txt", "response_path": "item.txt"},
				`{"status":200,"url":"https://api.external.example/v1/item.txt","response_path":"item.txt","written":true}`,
			)
			testutil.FailErr(t, "download item.txt", err)

			// Partial edit preserves external custody.
			_, _, err = backend.s.CommitEvidenceToolResult(
				ctx, s2.ID, dir, "edit",
				map[string]any{"path": "item.txt"},
				`{"path":"item.txt","applied":true}`,
			)
			testutil.FailErr(t, "edit item.txt", err)
			r1Handle, _, err := backend.s.CommitEvidenceToolResult(
				ctx, s2.ID, dir, "read",
				map[string]any{"path": "item.txt"},
				`{"path":"item.txt","content":"1|edited payload\n","offset":1,"end_line":1}`,
			)
			testutil.FailErr(t, "read after edit", err)

			// Full write replaces external custody so a later read of the replaced file does not inherit stale external provenance.
			_, _, err = backend.s.CommitEvidenceToolResult(
				ctx, s2.ID, dir, "write",
				map[string]any{"path": "item.txt"},
				`{"path":"item.txt","bytes_written":10}`,
			)
			testutil.FailErr(t, "full write item.txt", err)
			r2Handle, _, err := backend.s.CommitEvidenceToolResult(
				ctx, s2.ID, dir, "read",
				map[string]any{"path": "item.txt"},
				`{"path":"item.txt","content":"1|clean local\n","offset":1,"end_line":1}`,
			)
			testutil.FailErr(t, "read after full write", err)

			ev2, err := backend.s.LoadLedger(ctx, s2.ID)
			testutil.FailErr(t, "LoadLedger", err)
			rec1, ok1 := evidence.ResolveHandle(ev2, r1Handle)
			rec2, ok2 := evidence.ResolveHandle(ev2, r2Handle)
			if !ok1 || !rec1.UntrustedContent {
				t.Fatalf("%s: read after partial edit must retain UntrustedContent: %+v", backend.name, rec1)
			}
			if !ok2 || rec2.UntrustedContent || rec2.ExternalCustody {
				t.Fatalf("%s: read after full local write must not inherit stale external custody: %+v", backend.name, rec2)
			}
		})
	}

	// 4. Bounded metadata ledger filtering: non-custody read/grep rows are skipped without retention in buildMetadataLedgerFromRows.
	rows, err := store.queries.ListEvidenceRecords(ctx, sess.ID)
	testutil.FailErr(t, "ListEvidenceRecords", err)
	metaLedger, err := buildMetadataLedgerFromRows(rows)
	testutil.FailErr(t, "buildMetadataLedgerFromRows", err)
	if _, hasRead := metaLedger.Handles[readHandle]; hasRead {
		t.Fatalf("buildMetadataLedgerFromRows retained non-custody read record %s", readHandle)
	}
}

func TestStoredLineRangesPayload_LegacyDecodeAndVersionGate(t *testing.T) {
	// 1. Legacy v=1 JSON payload written before untrusted_content/external_custody existed decodes with false flags and intact metadata.
	const legacyRaw = `{"v":1,"ranges":[{"Start":1,"End":5}],"paths_touched":["saved.txt"],"urls_touched":["https://api.external.example/v1/item"],"surface":"http"}`
	var legacyRec evidence.Record
	testutil.FailErr(t, "unmarshal legacy v=1 payload", unmarshalEvidenceLineRanges(legacyRaw, &legacyRec))
	if legacyRec.UntrustedContent || legacyRec.ExternalCustody {
		t.Fatalf("legacy v=1 payload must default provenance flags to false: %+v", legacyRec)
	}
	if legacyRec.Surface != "http" || len(legacyRec.LineRanges) != 1 || legacyRec.LineRanges[0].End != 5 {
		t.Fatalf("legacy v=1 payload lost existing fields: %+v", legacyRec)
	}
	if !slices.Equal(evidence.IndexedPathsForRecord(legacyRec), []string{"saved.txt"}) ||
		!slices.Equal(evidence.ObservedURLsForRecord(legacyRec), []string{"https://api.external.example/v1/item"}) {
		t.Fatalf("legacy v=1 payload lost paths/urls: paths=%v urls=%v",
			evidence.IndexedPathsForRecord(legacyRec), evidence.ObservedURLsForRecord(legacyRec))
	}

	// 2. Clean records omit untrusted_content and external_custody keys (omitempty), while provenance records persist and round-trip them under v=1.
	cleanRaw, err := marshalEvidenceLineRanges(legacyRec)
	testutil.FailErr(t, "marshal clean record", err)
	if strings.Contains(cleanRaw, "untrusted_content") || strings.Contains(cleanRaw, "external_custody") {
		t.Fatalf("clean record must omit false provenance keys, got %s", cleanRaw)
	}

	provRec := legacyRec
	provRec.UntrustedContent = true
	provRec.ExternalCustody = true
	provRaw, err := marshalEvidenceLineRanges(provRec)
	testutil.FailErr(t, "marshal provenance record", err)
	if !strings.Contains(provRaw, `"v":1`) || !strings.Contains(provRaw, `"untrusted_content":true`) || !strings.Contains(provRaw, `"external_custody":true`) {
		t.Fatalf("provenance record JSON missing expected v=1 additive keys: %s", provRaw)
	}
	var roundTripped evidence.Record
	testutil.FailErr(t, "unmarshal provenance record", unmarshalEvidenceLineRanges(provRaw, &roundTripped))
	if !roundTripped.UntrustedContent || !roundTripped.ExternalCustody {
		t.Fatalf("provenance flags failed JSON round-trip: %+v", roundTripped)
	}

	// 3. Version gate refuses empty payloads, missing v (v=0), unsupported future versions (v=2), and malformed JSON.
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"empty", "", "missing evidence record payload"},
		{"missing_version_v0", `{"paths_touched":["saved.txt"]}`, "unsupported evidence record payload version 0"},
		{"unsupported_version_v2", `{"v":2,"paths_touched":["saved.txt"],"external_custody":true}`, "unsupported evidence record payload version 2"},
		{"malformed_json", `{"v":1,`, "unexpected end of JSON input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dst evidence.Record
			err := unmarshalEvidenceLineRanges(tc.raw, &dst)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("unmarshalEvidenceLineRanges(%q) err = %v, want substring %q", tc.raw, err, tc.wantErr)
			}
		})
	}

	// 4. Persisted legacy SQLite row loads cleanly through store.LoadLedger.
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO evidence_records (
			session_id, project_id, handle, ordinal, kind, shape, fidelity, source_tool, marks_untrusted,
			path, url, line_ranges, content_blob_sha256, truncated, survey, superseded_by
		) VALUES (?, ?, 'http_response#1', 1, 'http_response', 'surface_snapshot', 'structured', 'http_request', 0,
			'saved.txt', 'https://api.external.example/v1/item', ?, '', 0, 0, NULL)
	`, sess.ID, testdbseed.DefaultProjectID, legacyRaw)
	testutil.FailErr(t, "insert legacy row", err)

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger legacy row", err)
	got, ok := evidence.ResolveHandle(ev, "http_response#1")
	if !ok || got.UntrustedContent || got.ExternalCustody || got.Path != "saved.txt" {
		t.Fatalf("LoadLedger legacy row = %+v (ok=%v)", got, ok)
	}
}

func TestEvidenceLedgerStore_ExternalCustodyAndMetadataLookupBoundedWork(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	// 1. Seed 120 historical read/grep rows plus command, partial edit, and pathless inline http_response rows
	// with missing content_blob_sha256 values and an unsupported LineRanges version ({"v":99}) in the database,
	// alongside 1 live external-custody http_response row (saved.txt) and 1 live whole-file write row (replaced.txt).
	const poisonedNonCustodyJSON = `{"v":99}`
	for i := 1; i <= 60; i++ {
		readHandle := fmt.Sprintf("read#%d", i)
		grepHandle := fmt.Sprintf("grep#%d", i)
		missingReadSHA := fmt.Sprintf("%064x", i)
		missingGrepSHA := fmt.Sprintf("%064x", 1000+i)
		_, err := sqlDB.ExecContext(ctx, `
			INSERT INTO evidence_records (
				session_id, project_id, handle, ordinal, kind, shape, fidelity, source_tool, marks_untrusted,
				path, url, line_ranges, content_blob_sha256, truncated, survey, superseded_by
			) VALUES
				(?, ?, ?, ?, 'read', 'file_region', 'structured', 'read', 0, 'other.go', '', ?, ?, 0, 0, NULL),
				(?, ?, ?, ?, 'grep', 'file_region', 'structured', 'grep', 0, 'other.go', '', ?, ?, 0, 1, NULL)
		`,
			sess.ID, testdbseed.DefaultProjectID, readHandle, i, poisonedNonCustodyJSON, missingReadSHA,
			sess.ID, testdbseed.DefaultProjectID, grepHandle, i, poisonedNonCustodyJSON, missingGrepSHA,
		)
		testutil.FailErr(t, "insert read/grep rows", err)
	}
	// Also insert non-custody command, partial edit (write with source_tool=edit), and inline pathless http_response rows with poisoned LineRanges.
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO evidence_records (
			session_id, project_id, handle, ordinal, kind, shape, fidelity, source_tool, marks_untrusted,
			path, url, line_ranges, content_blob_sha256, truncated, survey, superseded_by
		) VALUES
			(?, ?, 'command#1', 1, 'command', 'command', 'scraped', 'command', 0, '', '', ?, ?, 0, 0, NULL),
			(?, ?, 'write#1', 1, 'write', 'structured_event', 'structured', 'edit', 0, 'saved.txt', '', ?, ?, 0, 0, NULL),
			(?, ?, 'http_response#1', 1, 'http_response', 'surface_snapshot', 'structured', 'http_request', 1, '', 'https://inline.external.example/v1', ?, ?, 0, 0, NULL)
	`,
		sess.ID, testdbseed.DefaultProjectID, poisonedNonCustodyJSON, fmt.Sprintf("%064x", 2001),
		sess.ID, testdbseed.DefaultProjectID, poisonedNonCustodyJSON, fmt.Sprintf("%064x", 2002),
		sess.ID, testdbseed.DefaultProjectID, poisonedNonCustodyJSON, fmt.Sprintf("%064x", 2003),
	)
	testutil.FailErr(t, "insert non-custody command/edit/inline-http rows", err)

	// Insert the 2 custody-affecting rows with valid v=1 LineRanges and missing content_blob_sha256 (proving 0 blob reads).
	custodyDownloadRec := evidence.BuildEvidenceRecord(
		dir,
		"http_request",
		map[string]any{"url": "https://api.external.example/v1/payload.txt", "response_path": "saved.txt"},
		`{"status":200,"url":"https://api.external.example/v1/payload.txt","response_path":"saved.txt","written":true}`,
	)
	custodyDownloadJSON, err := marshalEvidenceLineRanges(custodyDownloadRec)
	testutil.FailErr(t, "marshal custodyDownloadRec", err)

	wholeWriteRec := evidence.BuildEvidenceRecord(
		dir,
		"write",
		map[string]any{"path": "replaced.txt"},
		`{"path":"replaced.txt","bytes_written":12}`,
	)
	wholeWriteJSON, err := marshalEvidenceLineRanges(wholeWriteRec)
	testutil.FailErr(t, "marshal wholeWriteRec", err)

	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO evidence_records (
			session_id, project_id, handle, ordinal, kind, shape, fidelity, source_tool, marks_untrusted,
			path, url, line_ranges, content_blob_sha256, truncated, survey, superseded_by
		) VALUES
			(?, ?, 'http_response#2', 2, 'http_response', 'surface_snapshot', 'structured', 'http_request', 0, 'saved.txt', 'https://api.external.example/v1/payload.txt', ?, ?, 0, 0, NULL),
			(?, ?, 'write#2', 2, 'write', 'structured_event', 'structured', 'write', 0, 'replaced.txt', '', ?, ?, 0, 0, NULL)
	`,
		sess.ID, testdbseed.DefaultProjectID, custodyDownloadJSON, fmt.Sprintf("%064x", 3001),
		sess.ID, testdbseed.DefaultProjectID, wholeWriteJSON, fmt.Sprintf("%064x", 3002),
	)
	testutil.FailErr(t, "insert custody rows", err)

	// 2. Verify ListEvidenceRecords returns all 125 session rows, while buildMetadataLedgerFromRows decodes only the 2 custody-candidate rows
	// (succeeding despite poisoned LineRanges on all 123 non-custody rows and missing content blobs on all 125 rows) and retains only those 2 handles.
	rows, err := store.queries.ListEvidenceRecords(ctx, sess.ID)
	testutil.FailErr(t, "ListEvidenceRecords", err)
	if len(rows) != 125 {
		t.Fatalf("len(rows) = %d, want 125", len(rows))
	}
	metaLedger, err := buildMetadataLedgerFromRows(rows)
	testutil.FailErr(t, "buildMetadataLedgerFromRows", err)
	if len(metaLedger.Handles) != 2 {
		t.Fatalf("len(metaLedger.Handles) = %d, want 2 (handles=%v)", len(metaLedger.Handles), evidence.HandlesSorted(metaLedger))
	}
	if _, ok := metaLedger.Handles["http_response#2"]; !ok {
		t.Fatalf("missing http_response#2 in metaLedger: %v", evidence.HandlesSorted(metaLedger))
	}
	if _, ok := metaLedger.Handles["write#2"]; !ok {
		t.Fatalf("missing write#2 in metaLedger: %v", evidence.HandlesSorted(metaLedger))
	}

	// 3. Verify ExternalCustodyURLsForPaths and custody-only loadCommitLedgers (e.g., grep on saved.txt) succeed without decoding non-custody rows or reading blobs.
	urls, err := store.ExternalCustodyURLsForPaths(ctx, sess.ID, dir, []string{"saved.txt"})
	testutil.FailErr(t, "ExternalCustodyURLsForPaths", err)
	if !slices.Equal(urls, []string{"https://api.external.example/v1/payload.txt"}) {
		t.Fatalf("ExternalCustodyURLsForPaths(saved.txt) = %v, want [https://api.external.example/v1/payload.txt]", urls)
	}

	grepHandle, _, err := store.CommitEvidenceToolResult(
		ctx,
		sess.ID,
		dir,
		"grep",
		map[string]any{"pattern": "remote", "path": "saved.txt"},
		`{"matches":[{"path":"saved.txt","line":1,"content":"remote payload"}]}`,
	)
	testutil.FailErr(t, "CommitEvidenceToolResult grep on saved.txt", err)
	if grepHandle != "grep#61" {
		t.Fatalf("grepHandle = %q, want grep#61", grepHandle)
	}
	untrusted, err := store.SessionUntrustedContentResult(ctx, sess.ID)
	testutil.FailErr(t, "SessionUntrustedContentResult after grep", err)
	if !untrusted {
		t.Fatal("grep exposing lines from external-custody saved.txt must mark untrusted content")
	}

	// 4. Converse sensitivity check: if a live custody-candidate row (path-bearing http_response or whole-file write) has poisoned LineRanges,
	// buildMetadataLedgerFromRows DOES decode it and returns a decode error.
	poisonedCustodyRows := append([]db.ListEvidenceRecordsRow(nil), rows...)
	foundWrite := false
	for i := range poisonedCustodyRows {
		if poisonedCustodyRows[i].Handle == "write#2" {
			poisonedCustodyRows[i].LineRanges = poisonedNonCustodyJSON
			foundWrite = true
			break
		}
	}
	if !foundWrite {
		t.Fatal("missing write#2 in rows")
	}
	if _, err := buildMetadataLedgerFromRows(poisonedCustodyRows); err == nil {
		t.Fatal("expected buildMetadataLedgerFromRows to decode live whole-file write row and fail on poisoned JSON")
	}
}


