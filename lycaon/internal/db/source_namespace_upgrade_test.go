package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	seedReleasedNamespace(t, target, []namespaceSeed{
		{"tree", "", "directory"}, {"tree/nested", "", "directory"},
		{"tree/nested/file", "", "content"}, {"tree/nested/deleted", "", "absent"},
		{"tree/nested/unresolved", "", "unresolved"},
		{"tree/nested/file", "worker", "content"}, {"other/file", "worker", "absent"},
	})
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

type namespaceSeed struct{ path, branch, state string }

func seedReleasedNamespace(t *testing.T, path string, seeds []namespaceSeed) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released fixture for seeding", err)
	defer func() { _ = database.Close() }()
	testutil.FailErr(t, "enforce released fixture foreign keys", func() error { _, err := database.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); return err }())
	for i, seed := range seeds {
		id := fmt.Sprintf("namespace-%d", i)
		kind := "file"
		if seed.state == "directory" {
			kind = "directory"
		}
		_, err := database.ExecContext(t.Context(), `INSERT INTO source_files(id,project_id,entry_kind,created_ts) SELECT ?,project_id,?,created_ts FROM source_files LIMIT 1`, id, kind)
		testutil.FailErr(t, "seed logical file", err)
		_, err = database.ExecContext(t.Context(), `INSERT INTO source_versions(id,file_id,project_id,branch_id,root_id,path,state,content_sha256,capture_state,capture_reason,capture_quality,created_ts,seq)
            SELECT ?,?,project_id,?,root_id,?,?,CASE WHEN ?='content' THEN content_sha256 ELSE '' END,
            CASE WHEN ? IN ('directory','absent') THEN 'not_applicable' ELSE 'metadata_only' END,
            CASE WHEN ?='unresolved' THEN 'unavailable' ELSE '' END,'observed',created_ts,
            (SELECT max(seq)+1 FROM source_versions) FROM source_versions WHERE state='content' LIMIT 1`,
			id, id, seed.branch, seed.path, seed.state, seed.state, seed.state, seed.state)
		testutil.FailErr(t, "seed source version", err)
		_, err = database.ExecContext(t.Context(), `INSERT INTO source_branch_heads SELECT project_id,branch_id,file_id,id,root_id,path,state,content_sha256,seq,created_ts FROM source_versions WHERE id=?`, id)
		testutil.FailErr(t, "seed released branch head", err)
	}
}

func TestSourceNamespaceRefusesLossyReleasedPaths(t *testing.T) {
	for _, seed := range []namespaceSeed{{"tree/./file", "", "content"}, {"tree//nested", "", "directory"}} {
		t.Run(seed.path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", "1.0.1", "store.db"))
			testutil.FailErr(t, "read released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0600))
			seedReleasedNamespace(t, path, []namespaceSeed{seed})
			before, err := openReader(t.Context(), path)
			testutil.FailErr(t, "open seeded source", err)
			retained := namespaceTableContents(t, before, "source_branch_heads")
			baseline, err := inspectSchema(t.Context(), before)
			testutil.FailErr(t, "inspect source baseline", err)
			testutil.FailErr(t, "close source", before.Close())
			if err := UpgradeStaged(t.Context(), path); err == nil {
				t.Fatal("lossy path migration succeeded")
			}
			after, err := openReader(t.Context(), path)
			testutil.FailErr(t, "open refused source", err)
			defer func() { _ = after.Close() }()
			got, err := inspectSchema(t.Context(), after)
			testutil.FailErr(t, "inspect refused baseline", err)
			if got != baseline || namespaceTableContents(t, after, "source_branch_heads") != retained {
				t.Fatal("refused migration changed released history")
			}
		})
	}
}
