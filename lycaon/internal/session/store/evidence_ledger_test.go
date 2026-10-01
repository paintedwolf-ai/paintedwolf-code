package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"path/filepath"
	"strings"
	"testing"

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
