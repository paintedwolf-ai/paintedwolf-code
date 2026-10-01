package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegularTablesUseStrictKeyedStorage(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	rows, err := sqlDB.QueryContext(t.Context(), `
		SELECT name,
		       strict,
		       wr,
		       (SELECT COUNT(*) FROM pragma_table_info(tl.name) WHERE pk > 0) AS primary_key_columns
		FROM pragma_table_list AS tl
		WHERE schema = 'main' AND type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	testutil.FailErr(t, "inspect table storage", err)
	defer func() { _ = rows.Close() }()
	var violations []string
	for rows.Next() {
		var name string
		var strict, withoutRowID, primaryKeyColumns int
		testutil.FailErr(t, "scan table storage", rows.Scan(&name, &strict, &withoutRowID, &primaryKeyColumns))
		if strict == 0 {
			violations = append(violations, name+" is not STRICT")
		}
		if primaryKeyColumns == 0 {
			violations = append(violations, name+" has no primary key")
		}
		if primaryKeyColumns > 1 && withoutRowID == 0 {
			violations = append(violations, name+" has a composite primary key with rowid storage")
		}
	}
	testutil.FailErr(t, "iterate table storage", rows.Err())
	if len(violations) > 0 {
		t.Fatalf("table storage violations: %s", strings.Join(violations, "; "))
	}
}

func TestEveryForeignKeyChildHasLeadingIndex(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	rows, err := sqlDB.QueryContext(t.Context(), `
		SELECT name FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	testutil.FailErr(t, "list tables", err)
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var table string
		testutil.FailErr(t, "scan table", rows.Scan(&table))
		tables = append(tables, table)
	}
	testutil.FailErr(t, "iterate tables", rows.Err())

	var missing []string
	for _, table := range tables {
		foreignKeys := foreignKeyColumns(t, sqlDB, table)
		for _, columns := range foreignKeys {
			if !usesIndexedLookup(t, sqlDB, table, columns) {
				missing = append(missing, table+"("+strings.Join(columns, ",")+")")
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("foreign-key children without a leading index: %s", strings.Join(missing, "; "))
	}
}

func TestNoRedundantExplicitIndexes(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	rows, err := sqlDB.QueryContext(t.Context(), `
		WITH indexes AS (
		  SELECT s.name AS table_name, il.name AS index_name,
		         (SELECT group_concat(printf('%s:%d:%s', name, desc, coll), ',')
		            FROM (SELECT name, desc, coll
		                  FROM pragma_index_xinfo(il.name)
		                  WHERE key = 1 ORDER BY seqno)) AS signature
		  FROM sqlite_schema s, pragma_index_list(s.name) il
		  WHERE s.type = 'table' AND il.partial = 0
		    AND il.origin = 'c' AND il."unique" = 0
		)
		SELECT short.table_name, short.index_name, long.index_name
		FROM indexes short
		JOIN indexes long
		  ON long.table_name = short.table_name
		 AND long.index_name != short.index_name
		WHERE long.signature = short.signature
		   OR long.signature LIKE short.signature || ',%'
		ORDER BY short.table_name, short.index_name
	`)
	testutil.FailErr(t, "audit indexes", err)
	defer func() { _ = rows.Close() }()
	var redundant []string
	for rows.Next() {
		var table, shorter, longer string
		testutil.FailErr(t, "scan redundant index", rows.Scan(&table, &shorter, &longer))
		redundant = append(redundant, table+"."+shorter+" covered by "+longer)
	}
	testutil.FailErr(t, "iterate redundant indexes", rows.Err())
	if len(redundant) > 0 {
		t.Fatalf("redundant indexes: %s", strings.Join(redundant, "; "))
	}
}

func foreignKeyColumns(t *testing.T, sqlDB DBTX, table string) [][]string {
	t.Helper()
	rows, err := sqlDB.QueryContext(t.Context(), "PRAGMA foreign_key_list("+sqliteIdentifier(table)+")")
	testutil.FailErr(t, "foreign keys for "+table, err)
	defer func() { _ = rows.Close() }()
	byID := make(map[int][]string)
	for rows.Next() {
		var id, seq int
		var parent, from, to, onUpdate, onDelete, match string
		testutil.FailErr(t, "scan foreign key for "+table,
			rows.Scan(&id, &seq, &parent, &from, &to, &onUpdate, &onDelete, &match))
		columns := byID[id]
		if len(columns) != seq {
			t.Fatalf("%s foreign key %d has non-contiguous sequence %d", table, id, seq)
		}
		byID[id] = append(columns, from)
	}
	testutil.FailErr(t, "iterate foreign keys for "+table, rows.Err())
	out := make([][]string, 0, len(byID))
	for _, columns := range byID {
		out = append(out, columns)
	}
	return out
}

func usesIndexedLookup(t *testing.T, sqlDB DBTX, table string, columns []string) bool {
	t.Helper()
	predicates := make([]string, len(columns))
	args := make([]any, len(columns))
	for i, column := range columns {
		predicates[i] = sqliteIdentifier(column) + " = ?"
		args[i] = "indexed-value"
	}
	query := "EXPLAIN QUERY PLAN SELECT 1 FROM " + sqliteIdentifier(table) +
		" WHERE " + strings.Join(predicates, " AND ")
	rows, err := sqlDB.QueryContext(t.Context(), query, args...)
	testutil.FailErr(t, "plan foreign-key lookup for "+table, err)
	defer func() { _ = rows.Close() }()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		testutil.FailErr(t, "scan foreign-key lookup plan for "+table,
			rows.Scan(&id, &parent, &unused, &detail))
		indexed = indexed || strings.HasPrefix(detail, "SEARCH ")
	}
	testutil.FailErr(t, "iterate foreign-key lookup plan for "+table, rows.Err())
	return indexed
}

func sqliteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func TestSchemaRejectsCrossProjectRelationship(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	root1 := testdbseed.InsertProjectRoot(t, sqlDB, "project-1", t.TempDir())
	root2 := testdbseed.InsertProjectRoot(t, sqlDB, "project-2", t.TempDir())
	const now = "2026-01-01T00:00:00Z"

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at)
		VALUES ('bad-session', 'project-1', (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?)
	`, root2, now, now, now)
	assertConstraintContains(t, err, "workspace_root_id outside project")

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at)
		VALUES ('session-1', 'project-1', (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?)
	`, root1, now, now, now)
	testutil.FailErr(t, "insert valid session", err)
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
		VALUES ('bad-child', 'project-2', (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', 'session-1', ?, ?, ?)
	`, root2, now, now, now)
	assertConstraintContains(t, err, "parent_session_id outside project")

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO workflow_runs (
			id, session_id, project_id, workflow_id, workflow_version, status, created_at, updated_at
		) VALUES ('bad-run', 'session-1', 'project-2', 'wf', '1', 'running', ?, ?)
	`, now, now)
	assertConstraintContains(t, err, "project_id differs from session")

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO source_files (id, project_id, entry_kind, created_ts)
		VALUES ('file-1', 'project-1', 'file', ?)
	`, now)
	testutil.FailErr(t, "insert source file", err)
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO source_versions (
			id, file_id, project_id, root_id, path,
			state, content_sha256, byte_size, capture_state, capture_quality, created_ts, seq
		) VALUES ('bad-version', 'file-1', 'project-1', ?,
			'a.go', 'content', 'sha', 1, 'stored', 'exact', ?, 1)
	`, root2, now)
	assertConstraintContains(t, err, "source_versions root_id outside project")

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO session_worktrees (session_id, project_id, worktree_id, created_at)
		VALUES ('session-1', 'project-2', 'worktree-1', ?)
	`, now)
	assertConstraintContains(t, err, "project_id differs from session")
}

func TestProjectDeleteCascadesDurableProjectState(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	projectID := "project-delete"
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, projectID, t.TempDir())
	const now = "2026-01-01T00:00:00Z"
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at)
		VALUES ('delete-session', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?)
	`, projectID, rootID, now, now, now)
	testutil.FailErr(t, "seed session", err)
	seedProjectDeleteSourceHistory(t, sqlDB, projectID, rootID, now)
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO evidence_index (id, project_id, source, hit_kind, session_id)
		VALUES ('delete-evidence', ?, 'tool', 'evidence', 'delete-session');
		INSERT INTO worker_jobs (id, project_id, agent_type, status, prompt, brief, created_at)
		VALUES ('delete-worker', ?, 'implementer', 'complete', 'fixture', 'fixture', ?);
		INSERT INTO delegations (
			id, project_id, task, strategy, status, created_at
		) VALUES ('delete-delegation', ?, 'task', 'feature-based', 'done', ?);
		INSERT INTO delegation_legs (id, delegation_id, title, status, created_at)
		VALUES ('delete-leg', 'delete-delegation', 'leg', 'complete', ?);
	`,
		projectID,
		projectID, now,
		projectID, now,
		now,
	)
	testutil.FailErr(t, "seed project state", err)

	_, err = sqlDB.ExecContext(t.Context(), `DELETE FROM projects WHERE id = ?`, projectID)
	testutil.FailErr(t, "delete project", err)
	for table, want := range map[string]int{
		"project_roots": 0, "sessions": 0, "source_files": 0,
		"source_operations": 0, "source_versions": 0,
		"source_effects":                 0,
		"source_presentation_watermarks": 0, "source_agent_presentations": 0,
		"source_checkpoints": 0, "source_checkpoint_entries": 0,
		"source_line_attr": 0, "source_blob_objects": 1,
		"evidence_index": 0,
		"worker_jobs":    0, "delegations": 0, "delegation_legs": 0,
	} {
		var got int
		testutil.FailErr(t, "count "+table, sqlDB.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&got))
		if got != want {
			t.Fatalf("%s rows = %d want %d", table, got, want)
		}
	}
}

func TestProjectDeleteReleasesOnlyItsOwnRootScans(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	own, other, shared := t.TempDir(), t.TempDir(), t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, "project-deleted", own)
	testdbseed.InsertProjectRoot(t, sqlDB, "project-deleted", shared)
	testdbseed.InsertProjectRoot(t, sqlDB, "project-kept", other)
	testdbseed.InsertProjectRoot(t, sqlDB, "project-kept", shared)
	const now = "2026-01-01T00:00:00Z"
	for id, path := range map[string]string{"scan-own": own, "scan-other": other, "scan-shared": shared} {
		_, err = sqlDB.ExecContext(t.Context(), `
			INSERT INTO code_scans (id, canonical_path, categories_json, status, created_at, reuse_key)
			VALUES (?, ?, '["sast"]', 'complete', ?, ?)
		`, id, path, now, id)
		testutil.FailErr(t, "seed "+id, err)
	}

	_, err = sqlDB.ExecContext(t.Context(), `DELETE FROM projects WHERE id = 'project-deleted'`)
	testutil.FailErr(t, "delete project", err)
	rows, err := sqlDB.QueryContext(t.Context(), `SELECT id FROM code_scans ORDER BY id`)
	testutil.FailErr(t, "list scans", err)
	defer func() { _ = rows.Close() }()
	var kept []string
	for rows.Next() {
		var id string
		testutil.FailErr(t, "scan id", rows.Scan(&id))
		kept = append(kept, id)
	}
	testutil.FailErr(t, "iterate scans", rows.Err())
	if strings.Join(kept, ",") != "scan-other,scan-shared" {
		t.Fatalf("scans after project delete = %v, want [scan-other scan-shared]", kept)
	}
}

func seedProjectDeleteSourceHistory(t *testing.T, sqlDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, projectID, rootID, now string) {
	t.Helper()
	mustExec := func(step, statement string, args ...any) {
		t.Helper()
		_, err := sqlDB.ExecContext(t.Context(), statement, args...)
		testutil.FailErr(t, step, err)
	}
	mustExec("insert source blob", `
		INSERT INTO source_blob_objects (sha256, size, stored_size, storage_relpath, git_oid_sha1, git_oid_sha256)
		VALUES ('delete-sha', 1, 1, 'de/lete-sha.zst', 'delete-oid-sha1', 'delete-oid-sha256')
	`)
	mustExec("insert source file", `
		INSERT INTO source_files (id, project_id, entry_kind, created_ts)
		VALUES ('delete-file', ?, 'file', ?)
	`, projectID, now)
	mustExec("insert source operation", `
		INSERT INTO source_operations (
			id, project_id, origin, cause, session_id,
			capture_quality, started_ts, committed_ts
		) VALUES ('delete-operation', ?, 'agent', 'fixture', 'delete-session',
			'exact', ?, ?)
	`, projectID, now, now)
	mustExec("insert source pre-image", `
		INSERT INTO source_versions (
			id, file_id, project_id, root_id, path,
			state, content_sha256, byte_size, capture_state, capture_quality, created_ts, seq
		) VALUES ('delete-before', 'delete-file', ?, ?, 'a.go',
			'content', 'delete-sha', 1, 'stored', 'exact', ?, 1)
	`, projectID, rootID, now)
	mustExec("insert source post-image", `
		INSERT INTO source_versions (
			id, file_id, project_id, parent_version_id,
			operation_id, root_id, path, state, capture_state, capture_quality, created_ts, seq
		) VALUES ('delete-after', 'delete-file', ?, 'delete-before',
			'delete-operation', ?, 'a.go', 'absent', 'not_applicable', 'exact', ?, 2)
	`, projectID, rootID, now)
	mustExec("insert source effect", `
		INSERT INTO source_effects (
			id, project_id, operation_id, file_id, before_version_id, after_version_id,
			root_id, path, op, entry_kind, ordinal, created_ts
		) VALUES ('delete-effect', ?, 'delete-operation', 'delete-file', 'delete-before',
			'delete-after', ?, 'a.go', 'delete', 'file', 1, ?)
	`, projectID, rootID, now)
	mustExec("insert presentation watermark", `
		INSERT INTO source_presentation_watermarks (
			project_id, file_id, seen_after_ordinal, through_ordinal, displayed_effect_id, seen_ts
		) VALUES (?, 'delete-file', 0, 1, 'delete-effect', ?)
	`, projectID, now)
	mustExec("insert agent presentation", `
		INSERT INTO source_agent_presentations (effect_id, project_id, file_id, ordinal)
		VALUES ('delete-effect', ?, 'delete-file', 1)
	`, projectID)
	mustExec("insert source checkpoint", `
		INSERT INTO source_checkpoints (id, project_id, kind, created_ts)
		VALUES ('delete-checkpoint', ?, 'named', ?)
	`, projectID, now)
	mustExec("insert checkpoint entry", `
		INSERT INTO source_checkpoint_entries (checkpoint_id, file_id, version_id, ordinal)
		VALUES ('delete-checkpoint', 'delete-file', 'delete-after', 1)
	`)
	mustExec("insert line attribution", `
		INSERT INTO source_line_attr (project_id, file_id, start_line, end_line, effect_id)
		VALUES (?, 'delete-file', 1, 1, 'delete-effect')
	`, projectID)
}

func TestMessagesRejectDuplicatePositiveOrdAndSeq(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	testdbseed.InsertSession(t, sqlDB, "session", testdbseed.DefaultProjectID)
	const now = "2026-01-01T00:00:00Z"
	testdbseed.InsertSessionEntry(t, sqlDB, "entry-first", "session", "utterance", "first", 1)
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, seq, ord, ts)
		VALUES ('first', 'entry-first', 'session', 'user', 'a', 'user', 'user', 'trusted', 1, 1, ?)
	`, now)
	testutil.FailErr(t, "insert first message", err)
	testdbseed.InsertSessionEntry(t, sqlDB, "entry-duplicate-ord", "session", "utterance", "duplicate-ord", 2)
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, seq, ord, ts)
		VALUES ('duplicate-ord', 'entry-duplicate-ord', 'session', 'user', 'b', 'user', 'user', 'trusted', 2, 1, ?)
	`, now)
	assertConstraintContains(t, err, "UNIQUE")
	testdbseed.InsertSessionEntry(t, sqlDB, "entry-duplicate-seq", "session", "utterance", "duplicate-seq", 3)
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, seq, ord, ts)
		VALUES ('duplicate-seq', 'entry-duplicate-seq', 'session', 'user', 'b', 'user', 'user', 'trusted', 1, 2, ?)
	`, now)
	assertConstraintContains(t, err, "UNIQUE")
}

func TestSchemaRejectsTypeDriftAndMalformedJSON(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO projects (id, last_opened_at, created_at, roots_generation)
		VALUES ('bad-type', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'not-an-integer')
	`)
	assertConstraintContains(t, err, "cannot store TEXT value in INTEGER column")

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO projects (
			id, last_opened_at, created_at, trust_enabled
		) VALUES ('bad-json', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '{broken')
	`)
	assertConstraintContains(t, err, "json_valid(trust_enabled)")
}

func TestInvocationReceiptRejectsMalformedTerminalIsolation(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	testdbseed.InsertSession(t, sqlDB, "session", testdbseed.DefaultProjectID)

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO invocation_receipts (
			id, project_id, session_id, tool_call_id, tool_name,
			contract_digest, args_digest, owner, lifecycle, reversibility,
			evidence_policy, recovery_policy, status, invoked, evidence_kind,
			isolation_code, isolation_disposition, started_at
		) VALUES (
			'receipt', ?, 'session', 'call', 'command',
			'contract', 'args', 'processes', 'effect_attempt', 'recoverable',
			'attempt', 'abandon', 'completed', 1, 'attempt',
			?, 'control_plane', '2026-01-01T00:00:00Z'
		)
	`, testdbseed.DefaultProjectID, isolation.CodeControlPlaneDenied)
	assertConstraintContains(t, err, "CHECK constraint failed")

	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO invocation_receipts (
			id, project_id, session_id, tool_call_id, tool_name,
			contract_digest, args_digest, owner, lifecycle, reversibility,
			evidence_policy, recovery_policy, status, invoked, evidence_kind,
			isolation_code, isolation_disposition, failure_code, failure_class,
			failure_retryable, started_at
		) VALUES (
			'error-receipt', ?, 'session', 'error-call', 'command',
			'contract', 'args', 'processes', 'effect_attempt', 'recoverable',
			'attempt', 'abandon', 'error', 1, 'error',
			?, 'control_plane',
			?, 'isolation_rejection', 0,
			'2026-01-01T00:00:00Z'
		)
	`, testdbseed.DefaultProjectID, isolation.CodeControlPlaneDenied, isolation.CodeControlPlaneDenied)
	assertConstraintContains(t, err, "CHECK constraint failed")
}

// A review the owner raises at its effect seam stops an operation that ran, so
// a terminal rejection keeps the owner's invoked fact.
func TestInvocationReceiptAcceptsOwnerSeamTerminalRejection(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	testdbseed.InsertSession(t, sqlDB, "session", testdbseed.DefaultProjectID)

	for _, invoked := range []int{0, 1} {
		_, err = sqlDB.ExecContext(t.Context(), `
			INSERT INTO invocation_receipts (
				id, project_id, session_id, tool_call_id, tool_name,
				contract_digest, args_digest, owner, lifecycle, reversibility,
				evidence_policy, recovery_policy, status, invoked, evidence_kind,
				isolation_code, isolation_disposition, failure_code, failure_class,
				failure_retryable, started_at
			) VALUES (
				?, ?, 'session', ?, 'write',
				'contract', 'args', 'filesystem', 'journaled_mutation', 'recoverable',
				'journal', 'journal', 'rejected', ?, 'rejection',
				?, 'control_plane',
				?, 'isolation_rejection', 0,
				'2026-01-01T00:00:00Z'
			)
		`, fmt.Sprintf("receipt-%d", invoked), testdbseed.DefaultProjectID, fmt.Sprintf("call-%d", invoked), invoked,
			isolation.CodeControlPlaneDenied, isolation.CodeControlPlaneDenied)
		testutil.FailErr(t, fmt.Sprintf("insert terminal rejection invoked=%d", invoked), err)
	}
}

func assertConstraintContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("constraint error = %v, want text %q", err, want)
	}
}
