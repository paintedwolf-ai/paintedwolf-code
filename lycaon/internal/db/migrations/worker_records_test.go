package migrations

import (
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkerRecordsMigratesDelegationsAndLegDependencies(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open memory sqlite", err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)

	_, err = database.Exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE projects (id TEXT PRIMARY KEY);
		CREATE TABLE sessions (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_session_id TEXT);
		CREATE TABLE workflow_runs (id TEXT PRIMARY KEY);
		CREATE TABLE worker_jobs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			parent_session_id TEXT NOT NULL,
			child_session_id TEXT,
			agent_type TEXT NOT NULL,
			scope_json TEXT,
			brief TEXT NOT NULL,
			status TEXT NOT NULL,
			execution_target TEXT NOT NULL,
			workflow_run_id TEXT,
			workflow_phase TEXT NOT NULL DEFAULT '',
			workflow_work_id TEXT NOT NULL DEFAULT '',
			delegation_id TEXT,
			leg_id TEXT,
			merge_status TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			completed_at TEXT
		);
		CREATE TABLE worker_results (
			id TEXT PRIMARY KEY,
			worker_job_id TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE landed_changes (
			id TEXT PRIMARY KEY,
			worker_job_id TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE worker_prerequisites (
			worker_job_id TEXT NOT NULL,
			prerequisite_id TEXT NOT NULL
		);
		CREATE TABLE delegations (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			coordinator_session_id TEXT NOT NULL,
			task TEXT NOT NULL,
			status TEXT NOT NULL,
			phase TEXT,
			workflow_run_id TEXT,
			blueprint_path TEXT,
			base_head_sha TEXT,
			created_at TEXT NOT NULL
		);
		CREATE TABLE delegation_legs (
			id TEXT NOT NULL,
			delegation_id TEXT NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL,
			depends_on_json TEXT,
			files_json TEXT,
			completion_criteria_json TEXT,
			prompt TEXT,
			agent_type TEXT,
			worker_id TEXT,
			created_at TEXT NOT NULL,
			started_at TEXT,
			completed_at TEXT,
			PRIMARY KEY (delegation_id, id)
		);

		INSERT INTO projects VALUES ('proj-1');
		INSERT INTO sessions VALUES ('sess-1', 'proj-1', NULL), ('child-1', 'proj-1', 'sess-1');
		INSERT INTO workflow_runs VALUES ('run-1');

		INSERT INTO delegations VALUES
			('del-empty-new', 'proj-1', 'sess-1', 'empty task', 'canceled', 'plan', 'run-1', '.lycaon/plans/empty.md', 'sha-0', '2026-01-01T00:00:00Z'),
			('del-empty-bound', 'proj-1', 'sess-1', 'bound task', 'active', '', NULL, '.lycaon/plans/bound.md', 'sha-1', '2026-01-01T00:01:00Z'),
			('del-legs', 'proj-1', 'sess-1', 'planned work', 'active', 'work', 'run-1', '.lycaon/plans/work.md', 'sha-2', '2026-01-01T00:02:00Z');

		INSERT INTO worker_jobs VALUES (
			'job-bound', 'proj-1', 'sess-1', 'child-1', 'implementer', '{"mode":"write"}',
			'existing brief', 'complete', 'local', NULL, '', '', 'del-empty-bound', NULL,
			'merged', '2026-01-01T00:01:10Z', '2026-01-01T00:01:20Z'
		);
		INSERT INTO worker_results VALUES ('res-bound', 'job-bound', '2026-01-01T00:01:20Z');
		INSERT INTO landed_changes VALUES ('land-bound', 'job-bound', '2026-01-01T00:01:25Z');

		INSERT INTO delegation_legs VALUES
			('leg-a', 'del-legs', 'First title', 'pending', '', '["pkg/a.go"]', '["job:complete"]', '', 'implementer', '', '2026-01-01T00:02:01Z', '', ''),
			('leg-b', 'del-legs', 'Second title', 'held', '["leg-a"]', 'not-json', '[]', 'Implement second leg', '', '', '2026-01-01T00:02:02Z', '', ''),
			('leg-done', 'del-legs', 'Done title', 'complete', 'invalid', '[]', '[]', 'Done prompt', 'surveyor', '', '2026-01-01T00:02:03Z', '2026-01-01T00:02:04Z', '2026-01-01T00:02:05Z');
	`)
	testutil.FailErr(t, "seed legacy worker and delegation fixture", err)

	part := WorkerRecords()
	writes, err := part.Writes(t.Context(), database, 0)
	testutil.FailErr(t, "estimate worker record writes", err)
	if writes == 0 {
		t.Fatal("expected positive write bound")
	}

	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin migration tx", err)
	defer func() { _ = tx.Rollback() }()

	testutil.FailErr(t, "apply WorkerRecords", part.Apply(t.Context(), tx))
	testutil.FailErr(t, "validate WorkerRecords", part.Validate(t.Context(), tx))
	testutil.FailErr(t, "commit migration tx", tx.Commit())

	var workerCount, depCount int
	testutil.FailErr(t, "count migrated workers", database.QueryRow(`SELECT COUNT(*) FROM workers`).Scan(&workerCount))
	if workerCount != 5 {
		t.Fatalf("migrated workers = %d, want 5", workerCount)
	}
	testutil.FailErr(t, "count migrated dependencies", database.QueryRow(`SELECT COUNT(*) FROM worker_dependencies`).Scan(&depCount))
	if depCount != 1 {
		t.Fatalf("migrated dependencies = %d, want 1", depCount)
	}
}
