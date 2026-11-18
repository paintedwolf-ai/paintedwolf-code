package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRevision6BackfillsSessionModeFromRetainedAdmission(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "store.db")
	source := releasedRevision4Store(t, path)
	_, err := source.ExecContext(ctx, `
		INSERT INTO sessions(id, project_id, owner_person_id, posture, parent_session_id, created_at, activity_at, updated_at)
		SELECT 'old-worker', project_id, owner_person_id, posture, id, created_at, activity_at, updated_at
		FROM sessions WHERE id = 'fixture-session';
		INSERT INTO sessions(id, project_id, owner_person_id, posture, created_at, activity_at, updated_at)
		SELECT 'empty-chat', project_id, owner_person_id, posture, created_at, activity_at, updated_at
		FROM sessions WHERE id = 'fixture-session';
		INSERT INTO prompt_submissions(id, session_id, admission_seq, project_id, input_digest, input_json, status, origin, created_at)
		SELECT 'prior-prompt', id, 1, project_id, 'fixture', '{}', 'complete', 'loop_wake', created_at
		FROM sessions WHERE id = 'old-worker';
		INSERT INTO workflow_runs (id, session_id, project_id, workflow_id, workflow_version, status, attach_policy, current_phase, created_at, updated_at)
		SELECT 'dormant-run', id, project_id, 'fixture', '1', 'running', 'session_create', 'initial', created_at, updated_at
		FROM sessions WHERE id = 'empty-chat';`)
	testutil.FailErr(t, "seed released chat activity", err)
	testutil.FailErr(t, "close released store", source.Close())
	testutil.FailErr(t, "upgrade store", UpgradeStaged(ctx, path))

	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer upgraded.Close()
	var applied int
	err = upgraded.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations
		WHERE id = 'session-mode-authority' AND from_revision = 5 AND to_revision = 6`).Scan(&applied)
	testutil.FailErr(t, "read session mode migration ledger", err)
	if applied != 1 {
		t.Fatalf("session mode migration applied %d times", applied)
	}

	for _, want := range []struct {
		id, root, mode string
		started        int
	}{
		{id: "fixture-session", root: "fixture-session", mode: "agent", started: 1},
		{id: "old-worker", root: "fixture-session"},
		{id: "empty-chat", root: "empty-chat", mode: "agent"},
	} {
		var root, mode string
		var started int
		err := upgraded.QueryRowContext(ctx, "SELECT root_session_id, COALESCE(mode, ''), started FROM sessions WHERE id = ?", want.id).
			Scan(&root, &mode, &started)
		testutil.FailErr(t, "read backfilled session mode", err)
		if root != want.root || mode != want.mode || started != want.started {
			t.Fatalf("%s: got root=%q mode=%q started=%d, want %+v", want.id, root, mode, started, want)
		}
	}
	for _, update := range []string{
		"UPDATE sessions SET started = 0 WHERE id = 'fixture-session'",
		"UPDATE sessions SET mode = 'ask' WHERE id = 'fixture-session'",
	} {
		if _, err := upgraded.ExecContext(ctx, update); err == nil {
			t.Fatalf("SQLite accepted an invalid mode transition: %s", update)
		}
	}
}
