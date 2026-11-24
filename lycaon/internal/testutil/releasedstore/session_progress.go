package releasedstore

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// SeedSessionProgress populates revision 4 checklist shapes in a released store,
// covering distinct goal headers with identical rows, a goal-only document,
// multi-state markers, and labels exceeding 240 runes.
func SeedSessionProgress(t *testing.T, database *sql.DB) {
	t.Helper()
	ctx := t.Context()
	var hasContent int
	testutil.FailErr(t, "check session_progress.content column", database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('session_progress') WHERE name = 'content'`,
	).Scan(&hasContent))
	if hasContent == 0 {
		return
	}
	const at = "2026-10-01T00:00:00.000000000Z"
	var project, owner string
	testutil.FailErr(t, "read project and owner", database.QueryRowContext(ctx,
		`SELECT project_id, owner_person_id FROM sessions WHERE owner_person_id IS NOT NULL ORDER BY id LIMIT 1`,
	).Scan(&project, &owner))

	longLabel := "Verify end-to-end upgrade retention " + strings.Repeat("x", 580)
	seeds := []struct {
		id, runID, content string
	}{
		{
			id:      "00000000-0000-4000-8000-000000000021",
			runID:   "run-goal-a",
			content: "# Goal\n\nFirst request goal\n\n## Progress\n- [x] Ship change\n- [ ] Verify build\n",
		},
		{
			id:      "00000000-0000-4000-8000-000000000022",
			runID:   "run-goal-b",
			content: "# Goal\n\nDifferent request goal\n\n## Progress\n- [x] Ship change\n- [ ] Verify build\n",
		},
		{
			id:      "00000000-0000-4000-8000-000000000023",
			runID:   "run-goal-only",
			content: "# Goal\n\nSession goal pending.\n\n## Progress\n",
		},
		{
			id:      "00000000-0000-4000-8000-000000000024",
			runID:   "run-long-and-markers",
			content: "## Progress\n- [ ] " + longLabel + "\n- [~] Skip legacy shim\n- [>] Optional rollout note\n",
		},
	}
	for _, s := range seeds {
		_, err := database.ExecContext(ctx,
			`INSERT OR IGNORE INTO sessions(id, project_id, owner_person_id, posture, created_at, activity_at, updated_at) VALUES (?, ?, ?, 'build', ?, ?, ?)`,
			s.id, project, owner, at, at, at,
		)
		testutil.FailErr(t, "seed progress session", err)
		_, err = database.ExecContext(ctx,
			`INSERT OR REPLACE INTO session_progress(session_id, workflow_run_id, content, updated_at) VALUES (?, ?, ?, ?)`,
			s.id, s.runID, s.content, at,
		)
		testutil.FailErr(t, "seed released progress row", err)
	}
}
