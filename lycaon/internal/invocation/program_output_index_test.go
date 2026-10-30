package invocation_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramBlobRetentionUsesIndexedReceiptReferences(t *testing.T) {
	f := newProgramReceiptFixture(t)
	tx, err := f.database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin unrelated output rows", err)
	defer func() { _ = tx.Rollback() }()
	statement, err := tx.PrepareContext(t.Context(), `INSERT INTO invocation_program_outputs
		(id,program_id,stream,reference,retention_claim_id,initiating_claim_id,blob_id,blob_json,complete,redacted,mappings_json,created_at)
		VALUES(?,?, 'stdout', ?, ?, ?, ?, ?, 1, 0, '[]', '2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "prepare output rows", err)
	defer func() { testutil.FailErr(t, "close output statement on exit", statement.Close()) }()
	for i := range 1024 {
		id := fmt.Sprintf("blob-%04d", i)
		_, err = statement.ExecContext(t.Context(), fmt.Sprintf("output-%04d", i), f.program, id, "retention-"+id, "initiating-"+id, id, fmt.Sprintf(`{"ID":%q}`, id))
		testutil.FailErr(t, "insert output row", err)
	}
	testutil.FailErr(t, "close output statement", statement.Close())
	testutil.FailErr(t, "commit output rows", tx.Commit())

	store := f.recorder.ProgramBlobStore(blobstore.Store{})
	for _, tc := range []struct {
		id       string
		retained bool
	}{
		{"blob-0000", true},
		{"blob-1023", true},
		{"blob-1024", false},
		{"other", false},
	} {
		got, err := store.Retained(tc.id)
		testutil.FailErr(t, "check durable output retention", err)
		if got != tc.retained {
			t.Fatalf("retained(%q) = %v, want %v", tc.id, got, tc.retained)
		}
	}

	rows, err := f.database.QueryContext(t.Context(), `EXPLAIN QUERY PLAN SELECT EXISTS (SELECT 1 FROM invocation_program_outputs WHERE blob_id=?)`, "blob-1023")
	testutil.FailErr(t, "explain retention lookup", err)
	defer rows.Close()
	indexed := false
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		testutil.FailErr(t, "read retention plan", rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
		if strings.Contains(detail, "SCAN invocation_program_outputs") {
			t.Fatalf("retention lookup scans output rows: %s", detail)
		}
		indexed = indexed || strings.Contains(detail, "SEARCH invocation_program_outputs USING ") && strings.Contains(detail, "INDEX idx_invocation_program_outputs_blob_id")
	}
	testutil.FailErr(t, "finish retention plan", rows.Err())
	if !indexed {
		t.Fatalf("retention lookup did not use the blob ID index: %v", plan)
	}
}
