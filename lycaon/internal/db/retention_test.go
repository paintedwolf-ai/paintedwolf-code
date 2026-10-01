package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunRetentionPreservesProjectAndSessionHistory(t *testing.T) {
	ctx := t.Context()
	database := openTestDB(t)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	insertTestSession(t, database, "parent")
	old := FormatTime(time.Now().UTC().Add(-400 * 24 * time.Hour))
	_, err := database.ExecContext(ctx, `
INSERT INTO sessions(id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'parent', ?, ?, ?);
INSERT INTO worker_jobs(id, project_id, workspace_path, agent_type, status, prompt, brief, child_session_id, created_at, completed_at)
VALUES ('job', ?, '/tmp/project', 'implementer', 'complete', 'fixture', 'fixture', 'child', ?, ?);
INSERT INTO code_scans(id, canonical_path, categories_json, status, created_at, completed_at, trigger, reuse_key)
VALUES ('scan', '/tmp/project', '["sast"]', 'complete', ?, ?, 'write_burst', 'scan');
INSERT INTO security_full_passes(id, canonical_path, scanners_json, trigger, requested_at, started_at)
VALUES ('pass', '/tmp/project', '["lycaon-sast"]', 'manual', ?, ?);
`, testdbseed.DefaultProjectID, old, old, old, testdbseed.DefaultProjectID, old, old, old, old, old, old)
	testutil.FailErr(t, "seed retained history", err)
	_, err = database.ExecContext(ctx, `
INSERT INTO workflow_runs(id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at, completed_at)
VALUES ('run', 'child', ?, 'plan', '1.0.0', 'complete', 'done', ?, ?, ?);
INSERT INTO session_entries(id, session_id, ord, resource_kind, resource_id, created_at)
VALUES ('entry', 'child', 1, 'utterance', 'message', ?);
INSERT INTO messages(id, entry_id, session_id, role, content, origin, authority, trust_tier, workflow_run_id, ts)
VALUES ('message', 'entry', 'child', 'user', 'retained history', 'user', 'user', 'trusted', 'run', ?);
`, testdbseed.DefaultProjectID, old, old, old, old, old)
	testutil.FailErr(t, "seed retained workflow transcript", err)

	cfg := DefaultRetention()
	cfg.IncrementalVacuumMinFreelistPages = 9999
	_, err = RunRetention(ctx, database, cfg)
	testutil.FailErr(t, "RunRetention", err)
	for table, id := range map[string]string{"sessions": "child", "worker_jobs": "job", "code_scans": "scan", "security_full_passes": "pass", "workflow_runs": "run", "session_entries": "entry", "messages": "message"} {
		if !rowExists(t, database, "SELECT 1 FROM "+table+" WHERE id = ?", id) {
			t.Fatalf("%s history row %s was aged out", table, id)
		}
	}
}

func TestRunRetentionDisabledByEnv(t *testing.T) {
	t.Setenv("LYCAON_STORE_RETENTION", "0")
	if DefaultRetention().Enabled {
		t.Fatal("retention enabled despite LYCAON_STORE_RETENTION=0")
	}
}

func TestSourceRequestRetentionKeepsRecoveryReceipts(t *testing.T) {
	database := openTestDB(t)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	old := FormatTime(time.Now().UTC().Add(-40 * 24 * time.Hour))
	for _, state := range []string{"completed", "canceled", "failed", "interrupted", "running"} {
		var completed any
		if state != "running" {
			completed = old
		}
		_, err := database.ExecContext(t.Context(), `INSERT INTO source_file_requests(id,project_id,person_id,operation,method,uri,body,input_digest,root_scope,state,phase,created_at,updated_at,completed_at) VALUES(?,?,(SELECT id FROM people WHERE role='owner'),'deleteProjectSource','DELETE','/source',X'','digest','root',?,'preserving',?,?,?)`, state, testdbseed.DefaultProjectID, state, old, old, completed)
		testutil.FailErr(t, "seed request", err)
	}
	_, err := purgeOperationJournals(t.Context(), database, time.Now().UTC(), DefaultOperationJournalRetention, 500)
	testutil.FailErr(t, "expire receipts", err)
	for _, state := range []string{"completed", "canceled", "failed", "interrupted", "running"} {
		var count int
		testutil.FailErr(t, "read retained receipt", database.QueryRowContext(t.Context(), `SELECT count(*) FROM source_file_requests WHERE id=?`, state).Scan(&count))
		want := 1
		if state == "completed" || state == "canceled" {
			want = 0
		}
		if count != want {
			t.Fatalf("state=%s retained=%d want=%d", state, count, want)
		}
	}
}

func TestFreshStoreUsesIncrementalAutoVacuum(t *testing.T) {
	database := openTestDB(t)
	var mode int
	testutil.FailErr(t, "read auto_vacuum", database.QueryRowContext(t.Context(), `PRAGMA auto_vacuum`).Scan(&mode))
	if mode != 2 {
		t.Fatalf("auto_vacuum=%d want incremental(2)", mode)
	}
}

func TestRunRetentionAgesOutOnlyTerminalOperationReceipts(t *testing.T) {
	database := openTestDB(t)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	insertTestSession(t, database, "sess-1")
	old := FormatTime(time.Now().UTC().Add(-40 * 24 * time.Hour))
	fresh := FormatTime(time.Now().UTC().Add(-time.Hour))
	seedPromptSubmissionRow(t, database, "old", 1, "complete", old)
	seedPromptSubmissionRow(t, database, "fresh", 2, "complete", fresh)
	seedPromptSubmissionRow(t, database, "running", 3, "running", old)
	seedCommandInvocationRow(t, database, "command-old", old)

	cfg := DefaultRetention()
	cfg.IncrementalVacuumMinFreelistPages = 9999
	report, err := RunRetention(t.Context(), database, cfg)
	testutil.FailErr(t, "RunRetention", err)
	if report.JournalDeleted("prompt_submissions") != 1 || report.JournalDeleted("command_invocations") != 1 {
		t.Fatalf("journal cleanup report = %+v", report.OperationJournals)
	}
	if rowExists(t, database, `SELECT 1 FROM prompt_submissions WHERE id = 'old'`) {
		t.Fatal("expired terminal receipt remained")
	}
	for _, id := range []string{"fresh", "running"} {
		if !rowExists(t, database, `SELECT 1 FROM prompt_submissions WHERE id = ?`, id) {
			t.Fatalf("live receipt %s was removed", id)
		}
	}
}

func TestRunRetentionRemovesOnlyUnreachableLLMCalls(t *testing.T) {
	database := openTestDB(t)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	insertTestSession(t, database, "sess-1")
	seedLLMCallRow(t, database, "live", testdbseed.DefaultProjectID, "sess-1", time.Now().Add(-400*24*time.Hour))
	seedLLMCallRow(t, database, "old-session", testdbseed.DefaultProjectID, "gone", time.Now().Add(-400*24*time.Hour))
	seedLLMCallRow(t, database, "orphan", "gone-project", "gone", time.Now().Add(-400*24*time.Hour))
	cfg := DefaultRetention()
	cfg.IncrementalVacuumMinFreelistPages = 9999
	report, err := RunRetention(t.Context(), database, cfg)
	testutil.FailErr(t, "RunRetention", err)
	if report.OrphanLLMCalls != 1 {
		t.Fatalf("orphan calls removed=%d want 1", report.OrphanLLMCalls)
	}
	for _, id := range []string{"live", "old-session"} {
		if !rowExists(t, database, `SELECT 1 FROM llm_calls WHERE id = ?`, id) {
			t.Fatalf("cost history %s was removed", id)
		}
	}
}

func TestRollupLLMCallIDsPreservesUnselectedReceipts(t *testing.T) {
	database := openTestDB(t)
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	insertTestSession(t, database, "sess-1")
	// Both fixtures share a UTC rollup day.
	old := time.Now().UTC().Add(-100 * 24 * time.Hour).Truncate(24 * time.Hour).Add(12 * time.Hour)
	seedReportedLLMCallRow(t, database, "aged-1", testdbseed.DefaultProjectID, "sess-1", "anthropic", "claude", old, 100, 20, 5_000_000)
	seedReportedLLMCallRow(t, database, "aged-2", testdbseed.DefaultProjectID, "sess-1", "anthropic", "claude", old.Add(time.Hour), 50, 10, 2_000_000)
	seedLLMCallRow(t, database, "fresh", testdbseed.DefaultProjectID, "sess-1", time.Now().Add(-time.Hour))

	seedLLMCallRow(t, database, "unselected", testdbseed.DefaultProjectID, "sess-1", old)
	selected := []string{"aged-1", "aged-2"}
	rolledUp, err := RollupLLMCallIDs(t.Context(), database, selected)
	testutil.FailErr(t, "roll up selected receipts", err)
	if rolledUp != 2 {
		t.Fatalf("rolled up=%d want 2", rolledUp)
	}
	for _, id := range []string{"aged-1", "aged-2"} {
		if rowExists(t, database, `SELECT 1 FROM llm_calls WHERE id = ?`, id) {
			t.Fatalf("aged detail row %s was not rolled up", id)
		}
	}
	for _, id := range []string{"fresh", "unselected"} {
		if !rowExists(t, database, `SELECT 1 FROM llm_calls WHERE id = ?`, id) {
			t.Fatalf("unselected receipt %s was removed", id)
		}
	}
	day := FormatTime(old)[:10]
	var callCount, promptTokens, completionTokens, estimatedNanoUSD int64
	err = database.QueryRowContext(t.Context(), `
		SELECT call_count, prompt_tokens, completion_tokens, estimated_nano_usd
		FROM llm_call_rollups
		WHERE project_id = ? AND session_id = 'sess-1' AND provider_id = 'anthropic' AND model = 'claude' AND day = ?
	`, testdbseed.DefaultProjectID, day).Scan(&callCount, &promptTokens, &completionTokens, &estimatedNanoUSD)
	testutil.FailErr(t, "read llm_call_rollups", err)
	if callCount != 2 || promptTokens != 150 || completionTokens != 30 || estimatedNanoUSD != 7_000_000 {
		t.Fatalf("rollup totals = (calls=%d prompt=%d completion=%d usd=%d) want (2,150,30,7000000)",
			callCount, promptTokens, completionTokens, estimatedNanoUSD)
	}

	rolledUp, err = RollupLLMCallIDs(t.Context(), database, selected)
	testutil.FailErr(t, "repeat selected rollup", err)
	if rolledUp != 0 {
		t.Fatalf("second pass rolled up=%d want 0", rolledUp)
	}
	rolledUp, err = RollupLLMCallIDs(t.Context(), database, nil)
	testutil.FailErr(t, "roll up empty selection", err)
	if rolledUp != 0 {
		t.Fatalf("empty selection rolled up=%d want 0", rolledUp)
	}
}

func TestRunRetentionOptimizesFTSOnAnInterval(t *testing.T) {
	database := openTestDB(t)
	cfg := DefaultRetention()
	cfg.IncrementalVacuumMinFreelistPages = 9999
	// Empty retention tables isolate the FTS maintenance latch.
	cfg.OperationJournals = 0

	report, err := RunRetention(t.Context(), database, cfg)
	testutil.FailErr(t, "RunRetention", err)
	if len(report.FTSOptimized) != 2 {
		t.Fatalf("first run optimized=%v want both tables", report.FTSOptimized)
	}

	report2, err := RunRetention(t.Context(), database, cfg)
	testutil.FailErr(t, "RunRetention second pass", err)
	if len(report2.FTSOptimized) != 0 {
		t.Fatalf("second pass within interval optimized=%v want none", report2.FTSOptimized)
	}
}

func insertTestSession(t *testing.T, database Handle, id string) {
	t.Helper()
	testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
}

func sessionIDExists(database DBTX, id string) (string, error) {
	var got string
	err := database.QueryRowContext(context.Background(), `SELECT id FROM sessions WHERE id = ?`, id).Scan(&got)
	return got, err
}

func openTestDB(t *testing.T) *Store {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "Open", err)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func seedPromptSubmissionRow(t *testing.T, database Handle, id string, admissionSeq int64, status, at string) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
INSERT INTO prompt_submissions (id, session_id, admission_seq, project_id, input_digest, input_json, status, created_at, completed_at, origin, submitted_by)
VALUES (?, 'sess-1', ?, ?, 'digest', '{}', ?, ?, ?, 'user', (SELECT id FROM people WHERE role = 'owner'))`,
		id, admissionSeq, testdbseed.DefaultProjectID, status, at, at)
	testutil.FailErr(t, "insert prompt submission", err)
}

func seedCommandInvocationRow(t *testing.T, database Handle, id, at string) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
INSERT INTO command_invocations (operation_id, input_digest, response_json, created_at)
VALUES (?, 'digest', '{}', ?)`, id, at)
	testutil.FailErr(t, "insert command invocation", err)
}

func seedLLMCallRow(t *testing.T, database Handle, id, projectID, sessionID string, at time.Time) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
INSERT INTO llm_calls (id, session_id, project_id, status, started_at)
VALUES (?, ?, ?, 'reported', ?)`, id, sessionID, projectID, FormatTime(at))
	testutil.FailErr(t, "insert llm call", err)
}

func seedReportedLLMCallRow(t *testing.T, database Handle, id, projectID, sessionID, providerID, model string, at time.Time, promptTokens, completionTokens, estimatedNanoUSD int64) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
INSERT INTO llm_calls (
	id, session_id, project_id, provider_id, model, status,
	prompt_tokens, completion_tokens, estimated_nano_usd, started_at, completed_at
) VALUES (?, ?, ?, ?, ?, 'reported', ?, ?, ?, ?, ?)`,
		id, sessionID, projectID, providerID, model, promptTokens, completionTokens, estimatedNanoUSD,
		FormatTime(at), FormatTime(at))
	testutil.FailErr(t, "insert reported llm call", err)
}

func rowExists(t *testing.T, database DBTX, query string, args ...any) bool {
	t.Helper()
	var one int
	err := database.QueryRowContext(t.Context(), query, args...).Scan(&one)
	if IsNoRows(err) {
		return false
	}
	testutil.FailErr(t, "probe row", err)
	return true
}
