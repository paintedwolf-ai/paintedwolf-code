package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	benchSessionID  = "bench-root"
	benchRunCount   = 500
	benchChainLen   = 10
	benchEntryCount = 50_000
	benchChunkRows  = 1000
)

// seedDeleteSessionTreeFixture spreads messages across nested runs and the session.
func seedDeleteSessionTreeFixture(t testing.TB, sessionID string, runCount, chainLen, entryCount, chunkRows int) *Store {
	t.Helper()
	ctx := context.Background()
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := FormatTime(time.Now().UTC())
	_, err = sqlDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO projects (id, name, last_opened_at, created_at, roots_generation)
		VALUES (?, NULL, ?, ?, 0)
	`, testdbseed.DefaultProjectID, now, now)
	testutil.FailErr(t, "insert project", err)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO sessions (id, project_id, owner_person_id, posture, status, created_at, activity_at, updated_at)
		VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', ?, ?, ?)
	`, sessionID, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert session", err)

	runIDs := make([]string, runCount)
	for i := range runIDs {
		runIDs[i] = fmt.Sprintf("bench-run-%05d", i)
	}

	setupTx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin fixture tx", err)
	insertBenchWorkflowRuns(t, setupTx, sessionID, runIDs, chainLen)
	insertBenchEntriesAndMessages(t, setupTx, sessionID, runIDs, entryCount, chunkRows)
	testutil.FailErr(t, "commit fixture tx", setupTx.Commit())
	return sqlDB
}

func TestDeleteSessionTreeNestedRuns(t *testing.T) {
	assertDeleteSessionTreeFixture(t, 30, benchChainLen, 2*benchChunkRows+1, benchChunkRows)
}

func assertDeleteSessionTreeFixture(t *testing.T, runCount, chainLen, entryCount, chunkRows int) {
	t.Helper()
	ctx := t.Context()
	sqlDB := seedDeleteSessionTreeFixture(t, benchSessionID, runCount, chainLen, entryCount, chunkRows)

	var preRuns, preMsgs, preEntries int
	testutil.FailErr(t, "count fixture workflow_runs", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workflow_runs WHERE session_id = ?`, benchSessionID).Scan(&preRuns))
	testutil.FailErr(t, "count fixture messages", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id = ?`, benchSessionID).Scan(&preMsgs))
	testutil.FailErr(t, "count fixture session_entries", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_entries WHERE session_id = ?`, benchSessionID).Scan(&preEntries))
	if preRuns != runCount || preMsgs != entryCount || preEntries != entryCount {
		t.Fatalf("fixture setup = runs=%d messages=%d entries=%d, want runs=%d messages=%d entries=%d",
			preRuns, preMsgs, preEntries, runCount, entryCount, entryCount)
	}

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin delete tx", err)
	_, err = DeleteSessionTree(ctx, tx, benchSessionID)
	testutil.FailErr(t, "DeleteSessionTree", err)
	testutil.FailErr(t, "commit delete tx", tx.Commit())

	var runs, msgs, sessionEntries int
	testutil.FailErr(t, "count workflow_runs after delete", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workflow_runs WHERE session_id = ?`, benchSessionID).Scan(&runs))
	testutil.FailErr(t, "count messages after delete", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id = ?`, benchSessionID).Scan(&msgs))
	testutil.FailErr(t, "count session_entries after delete", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_entries WHERE session_id = ?`, benchSessionID).Scan(&sessionEntries))
	if runs != 0 || msgs != 0 || sessionEntries != 0 {
		t.Fatalf("session deletion left rows: workflow_runs=%d messages=%d session_entries=%d",
			runs, msgs, sessionEntries)
	}
}

// BenchmarkDeleteSessionTreeBulkFixture excludes fixture setup from the measurement.
func BenchmarkDeleteSessionTreeBulkFixture(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		sessionID := fmt.Sprintf("%s-%d", benchSessionID, i)
		sqlDB := seedDeleteSessionTreeFixture(b, sessionID, benchRunCount, benchChainLen, benchEntryCount, benchChunkRows)
		tx, err := sqlDB.BeginTx(ctx, nil)
		testutil.FailErr(b, "begin delete tx", err)
		b.StartTimer()

		_, err = DeleteSessionTree(ctx, tx, sessionID)
		testutil.FailErr(b, "DeleteSessionTree", err)
		testutil.FailErr(b, "commit delete tx", tx.Commit())
	}
}

// Completed runs permit multiple chain roots under the active-run uniqueness constraints.
func insertBenchWorkflowRuns(t testing.TB, tx *sql.Tx, sessionID string, runIDs []string, chainLen int) {
	t.Helper()
	now := FormatTime(time.Now().UTC())
	var b strings.Builder
	b.WriteString(`INSERT INTO workflow_runs (
		id, session_id, project_id, workflow_id, workflow_version, status,
		parent_run_id, revision, current_phase, vars_json, created_at, updated_at, completed_at
	) VALUES `)
	args := make([]any, 0, len(runIDs)*13)
	for i, id := range runIDs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?)")
		var parent any
		if i%chainLen != 0 {
			parent = runIDs[i-1]
		}
		args = append(args, id, sessionID, testdbseed.DefaultProjectID, "implement", "1.0.0", "complete",
			parent, 1, "intake", "{}", now, now, now)
	}
	_, err := tx.ExecContext(context.Background(), b.String(), args...)
	testutil.FailErr(t, "insert bench workflow runs", err)
}

// Chunking keeps fixture inserts below the bound-parameter limit.
func insertBenchEntriesAndMessages(t testing.TB, tx *sql.Tx, sessionID string, runIDs []string, entryCount, chunkRows int) {
	t.Helper()
	now := FormatTime(time.Now().UTC())
	ctx := context.Background()
	for start := 0; start < entryCount; start += chunkRows {
		end := start + chunkRows
		if end > entryCount {
			end = entryCount
		}
		insertBenchChunk(t, ctx, tx, sessionID, runIDs, start, end, now)
	}
}

func insertBenchChunk(t testing.TB, ctx context.Context, tx *sql.Tx, sessionID string, runIDs []string, start, end int, now string) {
	t.Helper()
	n := end - start
	var entriesSQL strings.Builder
	entriesSQL.WriteString(`INSERT INTO session_entries (id, session_id, ord, resource_kind, resource_id, created_at) VALUES `)
	entryArgs := make([]any, 0, n*6)

	var messagesSQL strings.Builder
	messagesSQL.WriteString(`INSERT INTO messages (
		id, entry_id, session_id, role, content, origin, authority, trust_tier, workflow_run_id, ts
	) VALUES `)
	messageArgs := make([]any, 0, n*10)

	for i := start; i < end; i++ {
		if i > start {
			entriesSQL.WriteString(",")
			messagesSQL.WriteString(",")
		}
		id := fmt.Sprintf("bench-msg-%06d", i)
		entriesSQL.WriteString("(?,?,?,?,?,?)")
		entryArgs = append(entryArgs, id, sessionID, int64(i+1), "model_output", id, now)

		var runID any
		if i%5 != 0 {
			runID = runIDs[i%len(runIDs)]
		}
		messagesSQL.WriteString("(?,?,?,?,?,?,?,?,?,?)")
		messageArgs = append(messageArgs, id, id, sessionID, "assistant", "fixture", "model", "none", "trusted", runID, now)
	}

	_, err := tx.ExecContext(ctx, entriesSQL.String(), entryArgs...)
	testutil.FailErr(t, "insert bench session_entries chunk", err)
	_, err = tx.ExecContext(ctx, messagesSQL.String(), messageArgs...)
	testutil.FailErr(t, "insert bench messages chunk", err)
}
