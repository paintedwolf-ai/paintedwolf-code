package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectRowTablesPlaceEveryReferencingTableFirst(t *testing.T) {
	store := openTestDB(t)
	tables, err := projectRowTables(t.Context(), store)
	testutil.FailErr(t, "plan project rows", err)
	position := map[string]int{}
	for i, table := range tables {
		position[table.name] = i
	}
	names, err := queryStrings(t.Context(), store, `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	testutil.FailErr(t, "list tables", err)
	for _, name := range names {
		refs, err := foreignKeyRefs(t.Context(), store, name)
		testutil.FailErr(t, "read foreign keys", err)
		_, planned := position[name]
		ownsRows := slices.ContainsFunc(refs, func(ref foreignKeyRef) bool { return ref.parent == "projects" })
		if ownsRows != planned {
			t.Fatalf("table %s references projects=%v but planned=%v", name, ownsRows, planned)
		}
		for _, ref := range refs {
			if parent, ok := position[ref.parent]; ok && planned && ref.parent != name && position[name] > parent {
				t.Fatalf("%s references %s but is cleared after it", name, ref.parent)
			}
		}
	}
}

// statementRows records how many rows each statement removed directly.
type statementRows struct {
	DBTX
	peak       int64
	statements int
}

func (s *statementRows) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := s.DBTX.ExecContext(ctx, query, args...)
	if err == nil {
		rows, _ := result.RowsAffected()
		s.peak, s.statements = max(s.peak, rows), s.statements+1
	}
	return result, err
}

func TestDeleteProjectRowsClearsChainedHistoryInBoundedStatements(t *testing.T) {
	store := openTestDB(t)
	const files = 2*ProjectRowBatch + 300
	seedChainedHistory(t, store, "project-deleted", files)
	seedChainedHistory(t, store, "project-kept", 10)
	counting := &statementRows{DBTX: store}
	testutil.FailErr(t, "delete project rows", DeleteProjectRows(t.Context(), counting, "project-deleted"))
	if counting.peak > ProjectRowBatch {
		t.Fatalf("one statement removed %d rows, above %d", counting.peak, ProjectRowBatch)
	}
	tables, err := projectRowTables(t.Context(), store)
	testutil.FailErr(t, "plan project rows", err)
	for _, table := range tables {
		var count int64
		testutil.FailErr(t, "count rows", store.QueryRowContext(t.Context(),
			"SELECT count(*) FROM "+quoteIdent(table.name)+" WHERE "+quoteIdent(table.column)+" = ?", "project-deleted").Scan(&count)) // #nosec G202 -- names read from the schema
		if count != 0 {
			t.Fatalf("%s keeps %d rows of the deleted project", table.name, count)
		}
	}
	var kept int
	testutil.FailErr(t, "count kept versions", store.QueryRowContext(t.Context(), `SELECT count(*) FROM source_versions WHERE project_id = 'project-kept'`).Scan(&kept))
	if kept != 10 {
		t.Fatalf("the other project has %d versions, want 10", kept)
	}
	var projects int
	testutil.FailErr(t, "count projects", store.QueryRowContext(t.Context(), `SELECT count(*) FROM projects`).Scan(&projects))
	if projects != 2 {
		t.Fatalf("projects = %d, want both rows kept", projects)
	}
}

// seedChainedHistory gives a project files whose versions and directories each
// reference the one before, so any order but newest-first breaks a reference.
func seedChainedHistory(t *testing.T, store *Store, projectID string, files int) {
	t.Helper()
	root := testdbseed.InsertProjectRoot(t, store, projectID, filepath.Join(t.TempDir(), projectID))
	const now = "2026-11-17T00:00:00Z"
	parentVersion, parentDirectory := sql.NullString{}, sql.NullString{}
	for i := range files {
		file := fmt.Sprintf("%s-file-%d", projectID, i)
		version := fmt.Sprintf("%s-version-%d", projectID, i)
		directory := fmt.Sprintf("%s-directory-%d", projectID, i)
		_, err := store.ExecContext(t.Context(), `INSERT INTO source_files (id, project_id, entry_kind, created_ts) VALUES (?, ?, 'file', ?)`, file, projectID, now)
		testutil.FailErr(t, "insert file", err)
		_, err = store.ExecContext(t.Context(), `INSERT INTO source_versions
(id, file_id, project_id, parent_version_id, root_id, path, state, content_sha256, byte_size, capture_state, capture_quality, created_ts, seq)
VALUES (?, ?, ?, ?, ?, ?, 'content', ?, 1, 'metadata_only', 'exact', ?, ?)`,
			version, file, projectID, parentVersion, root, fmt.Sprintf("f%d", i), fmt.Sprintf("%064x", i), now, i+1)
		testutil.FailErr(t, "insert version", err)
		_, err = store.ExecContext(t.Context(), `INSERT INTO source_directories (id, project_id, root_id, parent_id, name, present, ordinal, observed_ts)
VALUES (?, ?, ?, ?, ?, 1, 0, ?)`, directory, projectID, root, parentDirectory, fmt.Sprintf("d%d", i), now)
		testutil.FailErr(t, "insert directory", err)
		parentVersion = sql.NullString{String: version, Valid: true}
		parentDirectory = sql.NullString{String: directory, Valid: true}
	}
}
