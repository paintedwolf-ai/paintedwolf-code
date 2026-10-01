package findings_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func newFindingsStore(t *testing.T, sessionIDs ...string) *findings.SQLStore {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	for _, sessionID := range sessionIDs {
		testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	}
	return findings.NewSQLStore(sqlDB)
}

func appendFinding(t *testing.T, store *findings.SQLStore, sessionID, agent, summary, ref string) bool {
	t.Helper()
	appended, err := store.Append(context.Background(), sessionID, agent, summary, ref, "")
	testutil.FailErr(t, "append finding", err)
	return appended
}

func listFindings(t *testing.T, store *findings.SQLStore, sessionID string, max int) []findings.Finding {
	t.Helper()
	rows, err := store.List(context.Background(), sessionID, max)
	testutil.FailErr(t, "list findings", err)
	return rows
}

func recentFindings(t *testing.T, store *findings.SQLStore, sessionID, exclude string, afterID int64, max int, since time.Time) ([]findings.Finding, int64) {
	t.Helper()
	rows, next, err := store.Recent(context.Background(), sessionID, exclude, afterID, max, since)
	testutil.FailErr(t, "recent findings", err)
	return rows, next
}

func TestSQLStore_AppendAndList(t *testing.T) {
	store := newFindingsStore(t, "/proj")
	pd := "/proj"
	appendFinding(t, store, pd, "job-a", "found the score class", "score.py")
	appendFinding(t, store, pd, "job-b", "wired the sound manager", "sound.py")

	got := listFindings(t, store, pd, 10)
	if len(got) != 2 {
		t.Fatalf("list = %d want 2", len(got))
	}
	if got[1].Agent != "job-b" || got[1].Summary != "wired the sound manager" {
		t.Fatalf("newest = %+v", got[1])
	}
	if got[0].Ref != "score.py" {
		t.Fatalf("oldest ref = %q want score.py", got[0].Ref)
	}
}

func TestSQLStoreRetainsFindingsForPaging(t *testing.T) {
	store := newFindingsStore(t, "/proj")
	for i := range 205 {
		appendFinding(t, store, "/proj", "job", fmt.Sprintf("finding-%03d", i), "")
	}

	got := listFindings(t, store, "/proj", 500)
	if len(got) != 205 || got[0].Summary != "finding-000" {
		t.Fatalf("retained findings = %d first = %q", len(got), got[0].Summary)
	}
}

func TestSQLStore_RecentCursorExcludesAuthor(t *testing.T) {
	store := newFindingsStore(t, "/proj")
	pd := "/proj"
	appendFinding(t, store, pd, "job-a", "a1", "")
	appendFinding(t, store, pd, "job-b", "b1", "")

	notes, cursor := recentFindings(t, store, pd, "job-a", 0, 10, time.Time{})
	if len(notes) != 1 || notes[0].Agent != "job-b" {
		t.Fatalf("recent excluding job-a = %+v", notes)
	}
	if cursor != 2 {
		t.Fatalf("cursor = %d want 2", cursor)
	}
	more, nextCursor := recentFindings(t, store, pd, "job-a", cursor, 10, time.Time{})
	if len(more) != 0 || nextCursor != 2 {
		t.Fatalf("recent after cursor = %+v cursor=%d", more, nextCursor)
	}
}

func TestSQLStore_RecentSinceSpawn(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "since.db")
	store := findings.NewSQLStore(sqlDB)
	pd := "/proj"
	testdbseed.InsertSession(t, sqlDB, pd, testdbseed.DefaultProjectID)
	oldTime := time.Now().UTC().Add(-time.Hour)
	_, err := sqlDB.ExecContext(t.Context(),
		`INSERT INTO findings (session_id, agent, summary, ref, created_at) VALUES (?, ?, ?, ?, ?)`,
		pd, "old-job", "prior run", "", oldTime.Format(time.RFC3339),
	)
	testutil.FailErr(t, "insert old finding", err)
	appendFinding(t, store, pd, "job-new", "after spawn", "")

	spawnAt := time.Now().UTC().Add(-time.Minute)
	notes, _ := recentFindings(t, store, pd, "", 0, 10, spawnAt)
	if len(notes) != 1 || notes[0].Agent != "job-new" {
		t.Fatalf("since-spawn recent = %+v", notes)
	}
	if got := listFindings(t, store, pd, 10); len(got) != 2 {
		t.Fatalf("worklog list = %d want 2 (since filter must not affect List)", len(got))
	}
}

func TestSQLStore_ScopedByProject(t *testing.T) {
	store := newFindingsStore(t, "/proj-a", "/proj-b")
	appendFinding(t, store, "/proj-a", "job", "note a", "")
	appendFinding(t, store, "/proj-b", "job", "note b", "")
	if got := listFindings(t, store, "/proj-a", 10); len(got) != 1 || got[0].Summary != "note a" {
		t.Fatalf("proj-a findings = %+v", got)
	}
}

func TestSQLStore_AppendIdempotent(t *testing.T) {
	store := newFindingsStore(t, "/proj")
	const session = "/proj"
	if !appendFinding(t, store, session, "job-a", "peer note", "src/main.go") {
		t.Fatal("first append should insert")
	}
	if appendFinding(t, store, session, "job-a", "peer note", "src/main.go") {
		t.Fatal("duplicate append should be idempotent")
	}
	if got := listFindings(t, store, session, 10); len(got) != 1 {
		t.Fatalf("list = %d want 1", len(got))
	}
}

func TestSQLDeliverySurvivesStoreRecreationAndResponseReplay(t *testing.T) {
	database := testdbfixture.Open(t, "delivery.db")
	testdbseed.InsertSession(t, database, "root", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs(id,project_id,parent_session_id,agent_type,status,prompt,brief,created_at) VALUES ('worker',?,'root','implementer','running','fixture','fixture',?)`, testdbseed.DefaultProjectID, time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "seed worker", err)
	store := findings.NewSQLStore(database)
	delivered := findings.Delivery{Cursor: 11, Notes: []findings.Finding{{ID: 11, Summary: "retained contract", Ref: "source.go", HasBody: true}}}
	testutil.FailErr(t, "commit delivery", store.CommitDelivery(t.Context(), "worker", "response", delivered))
	testutil.FailErr(t, "replay receipt", store.CommitDelivery(t.Context(), "worker", "response", findings.Delivery{Cursor: 99}))
	testutil.FailErr(t, "late older response", store.CommitDelivery(t.Context(), "worker", "older-response", findings.Delivery{Cursor: 4}))
	var count int
	testutil.FailErr(t, "count delivery state", database.QueryRowContext(t.Context(), `SELECT count(*) FROM worker_finding_delivery`).Scan(&count))
	if count != 1 {
		t.Fatalf("delivery state rows = %d", count)
	}
	recovered, err := findings.NewSQLStore(database).Delivery(t.Context(), "worker")
	testutil.FailErr(t, "recover delivery", err)
	if recovered.Cursor != 11 || len(recovered.Notes) != 1 || recovered.Notes[0].Summary != "retained contract" {
		t.Fatalf("recovered: %+v", recovered)
	}
}
