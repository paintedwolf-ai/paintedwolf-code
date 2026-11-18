package releasedstore

import (
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// SeedDelegations populates retained delegation history in a released store.
func SeedDelegations(t *testing.T, database *sql.DB) {
	t.Helper()
	var tableCount int
	testutil.FailErr(t, "check delegations table", database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name IN ('delegations','delegation_legs')`).Scan(&tableCount))
	if tableCount != 2 {
		return
	}
	var existing int
	testutil.FailErr(t, "count delegations", database.QueryRow(`SELECT COUNT(*) FROM delegations`).Scan(&existing))
	if existing > 0 {
		return
	}
	const at = "2026-10-01T00:00:00.000000000Z"
	var jobID, project, parentSession string
	testutil.FailErr(t, "read worker job", database.QueryRow(`SELECT id, project_id, parent_session_id FROM worker_jobs WHERE parent_session_id IS NOT NULL ORDER BY id LIMIT 1`).Scan(&jobID, &project, &parentSession))
	_, err := database.Exec(
		`INSERT INTO delegations(id,project_id,coordinator_session_id,task,strategy,status,blueprint_path,base_head_sha,created_at) VALUES ('00000000-0000-4000-8000-000000000010',?,?,'Ship corpus plan','feature-based','active','.lycaon/plans/corpus.md','deadbeef',?)`,
		project, parentSession, at,
	)
	testutil.FailErr(t, "seed released delegation", err)
	for _, leg := range []struct {
		id, title, status, deps, files, criteria, prompt, workerID string
	}{
		{"leg-1", "Update README", "complete", "", `["README.md"]`, `[{"type":"file:modified","target":"README.md"}]`, "Update README", jobID},
		{"leg-2", "Write worker note", "pending", `["leg-1"]`, `["worker-note.txt"]`, `[{"type":"file:modified","target":"worker-note.txt"}]`, "Write worker note", ""},
	} {
		var startedAt, completedAt, worker any
		if leg.status == "complete" {
			startedAt, completedAt = at, at
		}
		if leg.workerID != "" {
			worker = leg.workerID
		}
		var deps any
		if leg.deps != "" {
			deps = leg.deps
		}
		_, err := database.Exec(
			`INSERT INTO delegation_legs(id,delegation_id,title,status,depends_on_json,files_json,completion_criteria_json,prompt,agent_type,worker_id,created_at,started_at,completed_at) VALUES (?,'00000000-0000-4000-8000-000000000010',?,?,?,?,?,?,'implementer',?,?,?,?)`,
			leg.id, leg.title, leg.status, deps, leg.files, leg.criteria, leg.prompt, worker, at, startedAt, completedAt,
		)
		testutil.FailErr(t, "seed released delegation leg", err)
	}
}
