package releasedstore

import (
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// SeedSourceWorktrees populates a worktree row in pre-revision-5 corpus copies
// that do not already contain worktree history.
func SeedSourceWorktrees(t *testing.T, database *sql.DB) {
	t.Helper()
	ctx := t.Context()
	var version int
	testutil.FailErr(t, "read user_version", database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version))
	if version >= 5 {
		return
	}
	var tableCount int
	testutil.FailErr(t, "check source_worktrees table", database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='source_worktrees'`).Scan(&tableCount))
	if tableCount != 1 {
		return
	}
	var existing int
	testutil.FailErr(t, "count source_worktrees", database.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_worktrees`).Scan(&existing))
	if existing > 0 {
		return
	}
	var projectID string
	testutil.FailErr(t, "read project", database.QueryRowContext(ctx, `SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&projectID))
	const at = "2026-10-01T00:00:00.000000000Z"
	_, err := database.ExecContext(ctx,
		`INSERT INTO source_worktrees(id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at) VALUES ('00000000-0000-4000-8000-000000000020', ?, 'repo-corpus', '/corpus/repo', '/corpus/repo-wt', 'session/corpus', 'main', ?)`,
		projectID, at,
	)
	testutil.FailErr(t, "seed released source_worktree", err)
}
