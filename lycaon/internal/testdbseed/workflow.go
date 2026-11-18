package testdbseed

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// InsertSessionWorkflowRun creates a workflow for an existing session.
func InsertSessionWorkflowRun(t *testing.T, database store, runID, sessionID, status string) {
	t.Helper()
	now := formatTime(time.Now().UTC())
	result, err := database.ExecContext(t.Context(), `
		INSERT INTO workflow_runs (
			id, session_id, project_id, workflow_id, workflow_version,
			status, revision, current_phase, created_at, updated_at, completed_at
		) SELECT ?, id, project_id, 'test-workflow', '1.0.0', ?, 1, 'test-phase', ?, ?, CASE WHEN ? = 'complete' THEN ? ELSE NULL END
		FROM sessions WHERE id = ?
	`, runID, status, now, now, status, now, sessionID)
	testutil.FailErr(t, "insert session workflow", err)
	count, err := result.RowsAffected()
	testutil.FailErr(t, "count inserted workflows", err)
	if count != 1 {
		t.Fatalf("workflow session %q does not exist", sessionID)
	}
}
