package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Worker jobs are project records, so they outlive the deleted chat with the
// link cleared; delegations belong to the chat and go with it.
func TestDeleteSessionTreeClosesSessionReferences(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)

	insertTestSession(t, sqlDB, "coordinator")
	_, err := sqlDB.ExecContext(ctx, `
		INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
		VALUES ('worker-child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coordinator', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert child session", err)

	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO worker_jobs (
			id, project_id, workspace_path, agent_type, status, prompt, brief,
			parent_session_id, child_session_id, created_at
		) VALUES ('job-1', ?, '/tmp/project', 'implementer', 'complete', 'fixture', 'fixture', 'coordinator', 'worker-child', '2026-01-01T00:00:00Z')
	`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert worker job", err)

	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO delegations (
			id, project_id, workspace_path, coordinator_session_id, task, strategy, status, created_at
		) VALUES ('dep-1', ?, '/tmp/project', 'coordinator', 'ship it', 'file-based', 'active', '2026-01-01T00:00:00Z')
	`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert delegation", err)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin tx", err)
	removed, err := DeleteSessionTree(ctx, tx, "coordinator")
	testutil.FailErr(t, "DeleteSessionTree", err)
	testutil.FailErr(t, "commit", tx.Commit())
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want the coordinator and its worker child", removed)
	}

	for _, id := range []string{"coordinator", "worker-child"} {
		if _, err := sessionIDExists(sqlDB, id); err == nil {
			t.Fatalf("session %s survived the tree delete", id)
		}
	}

	var parent, child sql.NullString
	err = sqlDB.QueryRowContext(ctx,
		`SELECT parent_session_id, child_session_id FROM worker_jobs WHERE id = 'job-1'`).Scan(&parent, &child)
	testutil.FailErr(t, "read worker job after delete", err)
	if parent.Valid || child.Valid {
		t.Fatalf("worker job kept dangling session ids: parent=%v child=%v", parent, child)
	}

	var delegations int
	testutil.FailErr(t, "count delegations", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delegations WHERE id = 'dep-1'`).Scan(&delegations))
	if delegations != 0 {
		t.Fatal("delegation outlived its coordinator session")
	}
}

// TestDeleteSessionTreeSweepsWorkflowRunMessages keeps run messages with their run.
func TestDeleteSessionTreeSweepsWorkflowRunMessages(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)

	insertTestSession(t, sqlDB, "sess-runs")
	_, err := sqlDB.ExecContext(ctx, `
		INSERT INTO workflow_runs (
			id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at, completed_at
		) VALUES ('run-1', 'sess-runs', ?, 'implement', '1.0.0', 'complete', 'intake', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert workflow run", err)
	testdbseed.InsertSessionEntry(t, sqlDB, "entry-msg-1", "sess-runs", "model_output", "msg-1", 1)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO messages (
			id, entry_id, session_id, role, content, origin, authority, trust_tier, workflow_run_id, ts
		) VALUES ('msg-1', 'entry-msg-1', 'sess-runs', 'assistant', 'hello', 'model', 'none', 'trusted', 'run-1', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert message", err)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin tx", err)
	_, err = DeleteSessionTree(ctx, tx, "sess-runs")
	testutil.FailErr(t, "DeleteSessionTree", err)
	testutil.FailErr(t, "commit", tx.Commit())

	var runs, msgs int
	testutil.FailErr(t, "count runs", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workflow_runs WHERE id = 'run-1'`).Scan(&runs))
	testutil.FailErr(t, "count messages", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE id = 'msg-1'`).Scan(&msgs))
	if runs != 0 || msgs != 0 {
		t.Fatalf("run/message sweep left rows: runs=%d messages=%d", runs, msgs)
	}
}

// A missing root removes nothing, which callers map to "not found".
func TestDeleteSessionTreeReportsMissingRoot(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin tx", err)
	defer func() { _ = tx.Rollback() }()
	removed, err := DeleteSessionTree(ctx, tx, "does-not-exist")
	testutil.FailErr(t, "DeleteSessionTree", err)
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want none", removed)
	}
}
