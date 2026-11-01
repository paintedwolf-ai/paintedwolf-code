package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceOperationKeyLookupUsesTheExistingUniqueIndex(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open source store", err)
	t.Cleanup(func() { testutil.FailErr(t, "close source store", store.Close()) })
	_, err = store.ExecContext(t.Context(), `INSERT INTO projects(id,last_opened_at,created_at) VALUES('project','','')`)
	testutil.FailErr(t, "seed project", err)
	queries := New(store)
	for _, entry := range []struct{ id, key string }{{"unkeyed", ""}, {"keyed", "operation"}} {
		testutil.FailErr(t, "insert actual source operation", queries.InsertSourceOperation(t.Context(), InsertSourceOperationParams{ID: entry.id, ProjectID: "project", Origin: "agent", Cause: "agent_tool", OperationKey: entry.key, CaptureQuality: "exact"}))
	}
	row, err := queries.GetSourceOperationByKey(t.Context(), GetSourceOperationByKeyParams{ProjectID: "project", OperationKey: "operation"})
	testutil.FailErr(t, "lookup keyed operation", err)
	if row.ID != "keyed" {
		t.Fatalf("keyed lookup returned %q", row.ID)
	}
	_, err = queries.GetSourceOperationByKey(t.Context(), GetSourceOperationByKeyParams{ProjectID: "project", OperationKey: ""})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unkeyed operations acquired replay identity: %v", err)
	}
	rows, err := store.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+getSourceOperationByKey, "project", "new-operation")
	testutil.FailErr(t, "inspect actual lookup plan", err)
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		testutil.FailErr(t, "read actual lookup plan", rows.Scan(&id, &parent, &unused, &detail))
		if strings.Contains(detail, "idx_source_operations_key") {
			indexed = true
		}
		if strings.Contains(detail, "idx_source_operations_project") {
			t.Fatalf("key lookup scans project history: %s", detail)
		}
	}
	testutil.FailErr(t, "complete lookup plan", rows.Err())
	if !indexed {
		t.Fatal("lookup did not use the existing unique operation-key index")
	}
}
