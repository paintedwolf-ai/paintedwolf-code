package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceNamespaceMigratesReleasedHistory(t *testing.T) {
	original := filepath.Join("..", "..", "testdata", "upgrade-corpus", "1.0.1", "store.db")
	raw, err := os.ReadFile(original)
	testutil.FailErr(t, "read released store", err)
	target := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy released store", os.WriteFile(target, raw, 0600))
	before, err := openReader(t.Context(), target)
	testutil.FailErr(t, "open released history", err)
	tables := []string{"source_branch_heads", "source_files", "source_versions", "source_effects", "source_operations", "source_history_entries", "source_recovery_entries", "source_checkpoints", "source_checkpoint_git_states"}
	retained := map[string]string{}
	for _, table := range tables {
		retained[table] = namespaceTableContents(t, before, table)
	}
	testutil.FailErr(t, "close released history", before.Close())
	testutil.FailErr(t, "upgrade released store", UpgradeStaged(t.Context(), target))
	after, err := openReader(t.Context(), target)
	testutil.FailErr(t, "open upgraded history", err)
	defer func() { _ = after.Close() }()
	for _, table := range tables {
		if got := namespaceTableContents(t, after, table); got != retained[table] {
			t.Fatalf("migration changed retained %s", table)
		}
	}
	want, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect target baseline", err)
	got, err := inspectSchema(t.Context(), after)
	testutil.FailErr(t, "inspect upgraded schema", err)
	if got != want {
		t.Fatalf("upgrade shape = %+v, fresh = %+v", got, want)
	}
	t.Logf("namespace baseline: %+v", want)
	testutil.FailErr(t, "idempotent upgrade", UpgradeStaged(t.Context(), target))
	source, err := os.ReadFile(original)
	testutil.FailErr(t, "reread released corpus", err)
	if string(source) != string(raw) {
		t.Fatal("released corpus changed")
	}
}

func namespaceTableContents(t *testing.T, database *sql.DB, table string) string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT * FROM `+table+` ORDER BY 1,2,3`)
	testutil.FailErr(t, "read retained table", err)
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	testutil.FailErr(t, "read table columns", err)
	var records [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		testutil.FailErr(t, "scan retained row", rows.Scan(targets...))
		records = append(records, values)
	}
	testutil.FailErr(t, "finish retained table", rows.Err())
	data, err := json.Marshal(records)
	testutil.FailErr(t, "encode retained rows", err)
	return string(data)
}

func TestSourceNamespaceTargetsFreshSchema(t *testing.T) {
	fresh, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect fresh schema", err)
	source := migrations.SourceNamespace(fresh).From
	plan, err := PlanSchemaUpgrade(t.Context(), source)
	testutil.FailErr(t, "plan released namespace and review upgrade", err)
	if !plan.Required() || plan.Target != fresh {
		t.Fatalf("released upgrade target = %+v (required=%v), fresh schema = %+v", plan.Target, plan.Required(), fresh)
	}
}

func TestUnreleasedNamespaceShapeHasNoUpgradeRoute(t *testing.T) {
	candidate := migrations.Baseline{
		Revision: 2,
		Shape:    "e6cf216656c2571aa402f58e5d5fb545283b7e4f5736afad06d299f1232b5e96",
	}
	if _, err := PlanSchemaUpgrade(t.Context(), candidate); !errors.Is(err, migrations.ErrUnsupported) {
		t.Fatalf("unreleased namespace candidate upgrade = %v, want unsupported shape", err)
	}
}
