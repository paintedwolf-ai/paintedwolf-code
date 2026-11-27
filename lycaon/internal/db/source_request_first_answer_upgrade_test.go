package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRevision5BackfillsSourceRequestFirstAnswerKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := releasedRevision4Store(t, path)
	seed := []struct {
		id, state, completed string
	}{
		{"req-completed", "completed", "2026-01-01T00:00:01Z"},
		{"req-failed", "failed", "2026-01-01T00:00:01Z"},
		{"req-canceled", "canceled", "2026-01-01T00:00:01Z"},
		{"req-interrupted", "interrupted", "2026-01-01T00:00:01Z"},
		{"req-running", "running", ""},
	}
	for _, row := range seed {
		var completed any
		if row.completed != "" {
			completed = row.completed
		}
		_, err := source.ExecContext(t.Context(), `
			INSERT INTO source_file_requests(
				id,project_id,person_id,operation,method,uri,body,input_digest,root_scope,
				state,phase,response_status,response_body,created_at,updated_at,completed_at
			) VALUES(
				?,'fixture-project',(SELECT id FROM people WHERE role='owner'),
				'createProjectSourceEntry','POST','/source',X'','digest','root',
				?,'done',201,'{"path":"a"}','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z',?
			)`, row.id, row.state, completed)
		testutil.FailErr(t, "seed revision 4 source_file_requests "+row.id, err)
	}
	testutil.FailErr(t, "close source", source.Close())

	testutil.FailErr(t, "upgrade released source", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer upgraded.Close()

	diff, err := BaselineShapeDiff(t.Context(), upgraded)
	testutil.FailErr(t, "compare upgraded shape with fresh target", err)
	if len(diff) != 0 {
		t.Fatalf("upgraded shape differs: %v", diff)
	}

	want := map[string]string{
		"req-completed":   "result",
		"req-failed":      "result",
		"req-canceled":    "accepted",
		"req-interrupted": "accepted",
		"req-running":     "",
	}
	for id, wantKind := range want {
		var got string
		testutil.FailErr(t, "read backfilled first_answer_kind "+id, upgraded.QueryRowContext(t.Context(), `SELECT first_answer_kind FROM source_file_requests WHERE id=?`, id).Scan(&got))
		if got != wantKind {
			t.Fatalf("%s first_answer_kind = %q, want %q", id, got, wantKind)
		}
	}

	if _, err := upgraded.ExecContext(t.Context(), `UPDATE source_file_requests SET first_answer_kind='bogus' WHERE id='req-completed'`); err == nil {
		t.Fatal("CHECK constraint allowed invalid first_answer_kind")
	}
}
