package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

const retirementCorpus = "../../testdata/upgrade-corpus"

// shippedRawDrops are the drops released steps made before DropReleased
// existed. Their sources are checksummed and cannot change.
var shippedRawDrops = map[string]map[string]string{
	"presence_authenticators.sql": {
		"managed_secret_reveals": "rebuilt under the same name with a widened CHECK; every row is copied first",
		"vault_unlocks":          "rebuilt under the same name with a widened CHECK; every row is copied first",
	},
	"source_manifest_layout.sql": {
		"source_manifest_staging": "rebuilt under the same name for the per-root manifest layout; staging rows are ephemeral build scratch",
	},
	"source_namespace_projection.sql": {
		"rewind_operations.journal_path": "journals resolve from operation identity in the current installation's checkpoint tree",
	},
	"source_namespace.go": {
		"source_checkpoint_entries": "write-only delta; checkpoints resolve through created_ordinal and the effect ledger",
	},
	"vault_unlock_away.sql": {
		"vault_unlocks": "rebuilt under the same name with a widened end_reason CHECK; every row is copied first",
	},
}

var (
	dropTable  = regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?"?(\w+)"?`)
	dropColumn = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+"?(\w+)"?\s+DROP\s+(?:COLUMN\s+)?"?(\w+)"?`)
)

// releasedNames lists every table and table.column in a released corpus store.
func releasedNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, path := range retirementCorpusStores(t) {
		database := openCorpusCopy(t, path)
		for table, columns := range corpusColumns(t, database) {
			names[table] = true
			for _, column := range columns {
				names[table+"."+column] = true
			}
		}
		testutil.FailErr(t, "close corpus store", database.Close())
	}
	return names
}

func retirementCorpusStores(t *testing.T) []string {
	t.Helper()
	stores, err := filepath.Glob(filepath.Join(retirementCorpus, "*", "store.db"))
	testutil.FailErr(t, "list corpus stores", err)
	if len(stores) == 0 {
		t.Fatal("upgrade corpus has no stores")
	}
	return stores
}

func openCorpusCopy(t *testing.T, path string) *sql.DB {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read corpus store", err)
	copied := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy corpus store", os.WriteFile(copied, raw, 0o600))
	database, err := sql.Open("sqlite", copied)
	testutil.FailErr(t, "open corpus store", err)
	return database
}

func corpusColumns(t *testing.T, database *sql.DB) map[string][]string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT m.name, p.name FROM sqlite_schema m JOIN pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'`)
	testutil.FailErr(t, "read corpus columns", err)
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var table, column string
		testutil.FailErr(t, "scan corpus column", rows.Scan(&table, &column))
		out[table] = append(out[table], column)
	}
	testutil.FailErr(t, "finish corpus columns", rows.Err())
	return out
}

// A migration drops a released table or column only through DropReleased,
// apart from the shipped drops above.
func TestMigrationsDropReleasedNamesOnlyThroughTheHelper(t *testing.T) {
	released := releasedNames(t)
	sources, err := filepath.Glob("migrations/*")
	testutil.FailErr(t, "list migration sources", err)
	var checked []string
	for _, source := range sources {
		name := filepath.Base(source)
		if name == "retire.go" || strings.HasSuffix(name, "_test.go") || (!strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".sql")) {
			continue
		}
		checked = append(checked, name)
		raw, err := os.ReadFile(source)
		testutil.FailErr(t, "read migration source", err)
		for _, target := range rawDropTargets(string(raw)) {
			if !released[target] {
				continue
			}
			if reason := shippedRawDrops[name][target]; reason == "" {
				t.Errorf("%s drops released %s directly; use migrations.DropReleased with a registered retirement", name, target)
			}
		}
	}
	if len(checked) == 0 {
		t.Fatal("no migration sources examined")
	}
}

func rawDropTargets(text string) []string {
	var dropped []string
	for _, match := range dropColumn.FindAllStringSubmatch(text, -1) {
		dropped = append(dropped, match[1]+"."+match[2])
	}
	for _, match := range dropTable.FindAllStringSubmatch(text, -1) {
		dropped = append(dropped, match[1])
	}
	return dropped
}

// Every registered retirement is reproduced from the released corpus: some
// fixture must hold the rows, and after the upgrade Rebuild returns exactly
// what Capture read before it.
func TestRegisteredRetirementsReproduceReleasedCorpusRows(t *testing.T) {
	for step, retirements := range migrations.RegisteredRetirements() {
		for _, r := range retirements {
			t.Run(step+"/"+r.Table+"/"+r.Column, func(t *testing.T) {
				populated := 0
				for _, path := range retirementCorpusStores(t) {
					database := openCorpusCopy(t, path)
					columns := corpusColumns(t, database)
					if _, ok := columns[r.Table]; !ok || (r.Column != "" && !slices.Contains(columns[r.Table], r.Column)) {
						testutil.FailErr(t, "close corpus store", database.Close())
						continue
					}
					captured := retirementRows(t, database, r.Capture)
					var file string
					testutil.FailErr(t, "locate corpus copy", database.QueryRowContext(t.Context(), `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file))
					testutil.FailErr(t, "close corpus store", database.Close())
					if len(captured) == 0 {
						continue
					}
					populated++
					testutil.FailErr(t, "upgrade corpus store", UpgradeStaged(t.Context(), file))
					upgraded, err := openReader(t.Context(), file)
					testutil.FailErr(t, "open upgraded store", err)
					rebuilt := retirementRows(t, upgraded, r.Rebuild)
					testutil.FailErr(t, "close upgraded store", upgraded.Close())
					if !slices.Equal(rebuilt, captured) {
						t.Errorf("%s: rebuild %v, released rows %v", path, rebuilt, captured)
					}
				}
				if populated == 0 {
					t.Fatalf("no released corpus fixture holds %s %s rows; seed them when preparing the release corpus", r.Table, r.Column)
				}
			})
		}
	}
}

func retirementRows(t *testing.T, database *sql.DB, query string) []string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), query)
	testutil.FailErr(t, "read retirement rows", err)
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	testutil.FailErr(t, "read retirement columns", err)
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		testutil.FailErr(t, "scan retirement row", rows.Scan(targets...))
		encoded, err := json.Marshal(values)
		testutil.FailErr(t, "encode retirement row", err)
		out = append(out, string(encoded))
	}
	testutil.FailErr(t, "finish retirement rows", rows.Err())
	slices.Sort(out)
	return out
}
