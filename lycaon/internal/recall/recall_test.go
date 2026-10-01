package recall_test

import (
	"database/sql"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	coordinatorSession = "coord-1"
	workerSession      = "worker-1"
	siblingSession     = "worker-2"
	projectID          = testdbseed.DefaultProjectID
)

type fixture struct {
	db      db.ReadHandle
	svc     *recall.Service
	dataDir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	testdbseed.InsertSessionWithRoot(t, sqlDB, coordinatorSession, projectID, dir)
	insertChild(t, sqlDB, workerSession, coordinatorSession, "scout")
	insertChild(t, sqlDB, siblingSession, coordinatorSession, "implementer")

	svc := recall.NewService(sqlDB, dir)
	return &fixture{db: sqlDB, svc: svc, dataDir: dir}
}

func insertChild(t *testing.T, sqlDB db.Handle, id, parent, agent string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (id, project_id, owner_person_id, posture, status, agent_type, parent_session_id, created_at, activity_at, updated_at)
		VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', ?, ?, ?, ?, ?)
	`, id, projectID, agent, parent, now, now, now)
	testutil.FailErr(t, "insert child session", err)
}

func (f *fixture) seed(t *testing.T, rows ...search.IndexRow) {
	t.Helper()
	ctx := t.Context()
	tx, err := f.db.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	for i := range rows {
		rows[i].ProjectID = projectID
		if rows[i].TS == "" {
			rows[i].TS = time.Now().UTC().Format(time.RFC3339)
		}
	}
	testutil.FailErr(t, "upsert rows", search.NewStore().UpsertRows(ctx, tx, rows))
	testutil.FailErr(t, "Commit", tx.Commit())
}

func evidenceRow(id, sessionID, handle, path, snippet string) search.IndexRow {
	return search.IndexRow{
		ID:        id,
		Source:    search.SourceTool,
		HitKind:   search.HitKindEvidence,
		SessionID: sessionID,
		Handle:    handle,
		Kind:      "read",
		Path:      path,
		Snippet:   snippet,
	}
}

func (f *fixture) answer(t *testing.T, caller recall.Caller, query string, widen recall.Widen) *recall.Result {
	t.Helper()
	res, err := f.svc.Answer(t.Context(), recall.Request{Query: query, Widen: widen, Caller: caller})
	testutil.FailErr(t, "Answer", err)
	return res
}

func coordinator() recall.Caller {
	return recall.Caller{SessionID: coordinatorSession, ProjectID: projectID}
}

func worker(sessionID string) recall.Caller {
	return recall.Caller{SessionID: sessionID, ParentSessionID: coordinatorSession, ProjectID: projectID}
}

// TestCoordinatorReachesFinishedWorkerLeg is the case the tool exists for: a
// detail a bounded completion envelope could not carry is still addressable.
func TestCoordinatorReachesFinishedWorkerLeg(t *testing.T) {
	f := newFixture(t)
	f.seed(t, evidenceRow("e1", workerSession, "read#3", "internal/confine/confine.go", "resolveWriteRoots skips the jail check"))

	res := f.answer(t, coordinator(), "resolveWriteRoots", recall.WidenDefault)
	if res.Resolution != recall.ResolutionMatched {
		t.Fatalf("resolution = %q, want matched", res.Resolution)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(res.Hits))
	}
	hit := res.Hits[0]
	if hit.AgentType != "scout" {
		t.Errorf("agent_type = %q, want scout", hit.AgentType)
	}
	if hit.SessionID != workerSession {
		t.Errorf("session_id = %q, want %q", hit.SessionID, workerSession)
	}
	if hit.Mine {
		t.Error("a worker leg's row must not be marked mine")
	}
	if res.Facets == nil || res.Facets.ByAgent["scout"] != 1 {
		t.Errorf("facets = %+v, want one scout row", res.Facets)
	}
}

// TestWorkerCannotReachSibling holds the delegation boundary. A leg acts on what
// it was delegated; a peer's observations are not that.
func TestWorkerCannotReachSibling(t *testing.T) {
	f := newFixture(t)
	f.seed(t,
		evidenceRow("e1", siblingSession, "read#1", "a.go", "sibling observation"),
		evidenceRow("e2", coordinatorSession, "read#2", "b.go", "coordinator observation"),
		evidenceRow("e3", workerSession, "read#3", "c.go", "own observation"),
	)

	res := f.answer(t, worker(workerSession), "observation", recall.WidenDefault)
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want only the worker's own row", len(res.Hits))
	}
	if res.Hits[0].SessionID != workerSession {
		t.Fatalf("session_id = %q, want %q", res.Hits[0].SessionID, workerSession)
	}
	if !res.Hits[0].Mine {
		t.Error("a leg's own row must be marked mine")
	}
}

// A worker cannot widen its scope; the denial is a typed error, not a result.
func TestWorkerWidenIsDenied(t *testing.T) {
	f := newFixture(t)
	f.seed(t, evidenceRow("e1", siblingSession, "read#1", "a.go", "sibling observation"))

	res, err := f.svc.Answer(t.Context(), recall.Request{
		Query: "observation", Widen: recall.WidenProject, Caller: worker(workerSession),
	})
	if err == nil {
		t.Fatalf("widen from a worker leg returned a result (%+v), want a typed denial", res)
	}
	var denied *recall.ErrScopeDenied
	if !asDenied(err, &denied) {
		t.Fatalf("error = %T, want ErrScopeDenied", err)
	}
	if denied.Role != recall.RoleWorker {
		t.Errorf("denied role = %q, want worker", denied.Role)
	}
}

func asDenied(err error, target **recall.ErrScopeDenied) bool {
	var v *recall.ErrScopeDenied
	ok := errors.As(err, &v)
	if ok {
		*target = v
	}
	return ok
}

// A scope with no indexed rows resolves differently from a scope whose rows
// did not match, so a worker that never ran is not read as one that found nothing.
func TestEmptyScopeIsNotAnEmptyMatch(t *testing.T) {
	f := newFixture(t)

	empty := f.answer(t, coordinator(), "anything", recall.WidenDefault)
	if empty.Resolution != recall.ResolutionScopeEmpty {
		t.Fatalf("resolution = %q, want scope_empty", empty.Resolution)
	}

	f.seed(t, evidenceRow("e1", workerSession, "read#1", "a.go", "something else entirely"))
	missed := f.answer(t, coordinator(), "anything", recall.WidenDefault)
	if missed.Resolution != recall.ResolutionNoMatchInScope {
		t.Fatalf("resolution = %q, want no_match_in_scope", missed.Resolution)
	}
}

// TestDeletedSessionLeavesAReachableAnswer: an address that once resolved must
// not fail the same way an address that never existed does.
func TestDeletedSessionLeavesAReachableAnswer(t *testing.T) {
	f := newFixture(t)
	f.seed(t, evidenceRow("e1", workerSession, "read#3", "a.go", "the observation"))

	tx, err := f.db.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "BeginTx", err)
	_, delErr := db.DeleteSessionTree(t.Context(), tx, workerSession)
	testutil.FailErr(t, "DeleteSessionTree", delErr)
	testutil.FailErr(t, "Commit", tx.Commit())

	res := f.answer(t, coordinator(), "handle:read#3", recall.WidenDefault)
	if res.Resolution != recall.ResolutionRecordDeleted {
		t.Fatalf("resolution = %q, want record_deleted", res.Resolution)
	}
	if res.Removed == 0 {
		t.Error("a tombstoned row must be counted as removed")
	}
	if len(res.Hits) != 0 {
		t.Errorf("tombstones must never appear as hits, got %d", len(res.Hits))
	}

	// The row survives as identity only. Content columns are cleared, and the
	// UPDATE carries the clear into the FTS shadow through the schema trigger.
	var snippet, path sql.NullString
	testutil.FailErr(t, "tombstone row", f.db.QueryRowContext(t.Context(),
		`SELECT snippet, path FROM evidence_index WHERE id = 'e1' AND tombstoned = 1`).Scan(&snippet, &path))
	if snippet.Valid || path.Valid {
		t.Errorf("tombstone kept content: snippet=%v path=%v", snippet, path)
	}
	var ftsHits int
	testutil.FailErr(t, "fts probe", f.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM evidence_fts WHERE evidence_fts MATCH 'observation'`).Scan(&ftsHits))
	if ftsHits != 0 {
		t.Errorf("deleted content still searchable in FTS: %d rows", ftsHits)
	}
}

func TestLiveCodeIsRejected(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Answer(t.Context(), recall.Request{Query: "kind:code needle", Caller: coordinator()})
	if err == nil {
		t.Fatal("kind:code must reject, not silently search the record")
	}
	var live *recall.ErrLiveCodeRequested
	if !errors.As(err, &live) {
		t.Fatalf("error = %T, want ErrLiveCodeRequested", err)
	}
}

func TestTruncationIsStatedNotImplied(t *testing.T) {
	f := newFixture(t)
	rows := make([]search.IndexRow, 0, 5)
	for i := 0; i < 5; i++ {
		rows = append(rows, evidenceRow(
			"e"+string(rune('a'+i)), workerSession, "read#1", "a.go", "needle observation"))
	}
	f.seed(t, rows...)

	res, err := f.svc.Answer(t.Context(), recall.Request{Query: "needle", Limit: 2, Caller: coordinator()})
	testutil.FailErr(t, "Answer", err)
	if !res.Truncated {
		t.Fatal("a capped generation must say so")
	}
	if res.Count.Relation != recall.RelationAtLeast {
		t.Errorf("relation = %q, want at_least", res.Count.Relation)
	}
	if res.Facets != nil && res.Facets.Exhaustive {
		t.Error("facets counted over a truncated generation are not exhaustive")
	}
	if res.NextAction == "" {
		t.Error("a truncated answer must name how to narrow it")
	}
}

// Changed files mark earlier observations stale.
func TestChangedPathIsMarkedStale(t *testing.T) {
	f := newFixture(t)
	observed := time.Now().UTC().Add(-time.Hour)
	row := evidenceRow("e1", workerSession, "read#3", "internal/a.go", "the observation")
	row.TS = observed.Format(time.RFC3339)
	f.seed(t, row)
	seedSourceChange(t, f.db, "internal/a.go", "write", observed.Add(time.Minute))

	res := f.answer(t, coordinator(), "observation", recall.WidenDefault)
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(res.Hits))
	}
	if res.Hits[0].Currency != recall.CurrencyChanged {
		t.Fatalf("currency = %q, want changed", res.Hits[0].Currency)
	}
}

func seedSourceChange(t *testing.T, sqlDB db.Handle, path, op string, at time.Time) {
	t.Helper()
	var rootID string
	testutil.FailErr(t, "root lookup", sqlDB.QueryRowContext(t.Context(),
		`SELECT id FROM project_roots WHERE project_id = ? LIMIT 1`, projectID).Scan(&rootID))
	store := sourceledger.New(sqlDB, t.TempDir())
	testutil.FailErr(t, "record source effect", store.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: projectID,
		RootID:    rootID, Path: path, Op: api.SourceChangeOp(op),
		Origin: api.SourceChangeOriginAgent, OperationID: "sc-" + path + op,
		Before: []byte("before"), After: []byte("after"), TS: at,
	}))
}
