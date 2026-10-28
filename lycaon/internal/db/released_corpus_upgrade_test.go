package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

const releasedCorpusRoot = "../../testdata/upgrade-corpus"

// releasedCorpusStores lists every shipped corpus store so a new release
// fixture is exercised without editing this test.
func releasedCorpusStores(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(releasedCorpusRoot)
	testutil.FailErr(t, "list released corpus", err)
	var versions []string
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(releasedCorpusRoot, entry.Name(), "store.db")); err == nil {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) == 0 {
		t.Fatal("released corpus has no stores")
	}
	return versions
}

func copyReleasedStore(t *testing.T, version string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(releasedCorpusRoot, version, "store.db"))
	testutil.FailErr(t, "read released store", err)
	path := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0600))
	return path
}

type corpusSemantics struct {
	ProjectID string `json:"project_id"`
	Messages  []struct {
		ID      string `json:"id"`
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Artifact struct {
		ID     string `json:"id"`
		SHA256 string `json:"sha256"`
	} `json:"artifact"`
}

// tableColumns returns every user table and its columns in declaration order.
func tableColumns(t *testing.T, database *sql.DB) map[string][]string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	testutil.FailErr(t, "list tables", err)
	var tables []string
	for rows.Next() {
		var name string
		testutil.FailErr(t, "scan table", rows.Scan(&name))
		tables = append(tables, name)
	}
	testutil.FailErr(t, "finish tables", rows.Err())
	testutil.FailErr(t, "close tables", rows.Close())
	out := make(map[string][]string, len(tables))
	for _, table := range tables {
		columns, err := database.QueryContext(t.Context(), `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		testutil.FailErr(t, "read columns", err)
		for columns.Next() {
			var name string
			testutil.FailErr(t, "scan column", columns.Scan(&name))
			out[table] = append(out[table], name)
		}
		testutil.FailErr(t, "close columns", columns.Close())
	}
	return out
}

// projectedRows encodes a table's rows over the given columns, sorted so the
// comparison does not depend on storage order.
func projectedRows(t *testing.T, database *sql.DB, table string, columns []string) []string {
	t.Helper()
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = `"` + column + `"`
	}
	rows, err := database.QueryContext(t.Context(), `SELECT `+strings.Join(quoted, ",")+` FROM "`+table+`"`)
	testutil.FailErr(t, "read "+table, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		testutil.FailErr(t, "scan "+table, rows.Scan(targets...))
		encoded, err := json.Marshal(values)
		testutil.FailErr(t, "encode "+table, err)
		out = append(out, string(encoded))
	}
	testutil.FailErr(t, "finish "+table, rows.Err())
	slices.Sort(out)
	return out
}

func sharedColumns(before, after []string) []string {
	var out []string
	for _, column := range before {
		if slices.Contains(after, column) {
			out = append(out, column)
		}
	}
	return out
}

// reshapedByRegisteredSteps names released tables whose stored values a
// registered step rewrites; every other released row survives verbatim.
var reshapedByRegisteredSteps = map[string]string{
	"source_history_entries": "SourceNamespace moves plan identity into typed columns",
	"evidence_index":         "ToolResultPaths backfills recorded result paths into the derived search index",
	"evidence_fts":           "ToolResultPaths reindexes the derived search text",
}

// retiredColumnsByRegisteredSteps names released columns a registered step
// drops; the remaining columns keep their rows.
var retiredColumnsByRegisteredSteps = map[string][]string{
	"rewind_operations": {"journal_path"},
}

func retainedColumns(table string, columns []string) []string {
	var out []string
	for _, column := range columns {
		if !slices.Contains(retiredColumnsByRegisteredSteps[table], column) {
			out = append(out, column)
		}
	}
	return out
}

// sqliteShadowTable identifies FTS5 storage internals owned by their virtual table.
func sqliteShadowTable(table string) bool {
	for _, suffix := range []string{"_fts_data", "_fts_idx", "_fts_docsize", "_fts_config", "_fts_content"} {
		if strings.HasSuffix(table, suffix) {
			return true
		}
	}
	return false
}

// retiredByRegisteredSteps names released tables a registered step replaces,
// with the successor that carries their rows. SourceNamespace drops
// source_checkpoint_entries without a successor; checkpoints keep their
// snapshot identity in source_checkpoints.
var retiredByRegisteredSteps = map[string]string{
	"source_branch_heads":       "source_head_entries",
	"source_checkpoint_entries": "",
}

// seedReleasedCredentials writes device credential, vault and grant rows with
// the released column set; the corpus recipe leaves those tables empty.
func seedReleasedCredentials(t *testing.T, database *sql.DB) {
	t.Helper()
	const at = "2026-10-01T00:00:00.000000000Z"
	var person, project, session string
	testutil.FailErr(t, "read owner", database.QueryRow(`SELECT id FROM people WHERE role='owner'`).Scan(&person))
	testutil.FailErr(t, "read project", database.QueryRow(`SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&project))
	testutil.FailErr(t, "read session", database.QueryRow(`SELECT id FROM sessions WHERE parent_session_id IS NULL ORDER BY id LIMIT 1`).Scan(&session))
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO managed_secrets(id,project_id,created_by_person_id,scope,name,purpose,origin,operation_id,created_at) VALUES ('00000000-0000-4000-8000-000000000001',?,?,'project','deploy token','release uploads','settings_entered','settings-1',?)`, []any{project, person, at}},
		{`INSERT INTO managed_secret_versions(id,secret_id,version,created_at) VALUES ('00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000001',1,?)`, []any{at}},
		{`INSERT INTO vault_unlocks(id,project_id,chat_session_id,person_id,authenticator,window_label,unlocked_at,ended_at,end_reason) VALUES ('00000000-0000-4000-8000-000000000003',?,?,?,'macos_user_presence','main',?,?,'manual')`, []any{project, session, person, at, at}},
		{`INSERT INTO credential_authored_values(project_id,root_id,path,value_fingerprint,session_id,tool_call_id,created_at) VALUES (?,'root','config/.env','fingerprint',?,'call-1',?)`, []any{project, session, at}},
		{`INSERT INTO chat_grants(chat_session_id,id,checkpoint_id,delta_json,created_at) VALUES (?,'grant-1','checkpoint-1','{"hosts":["*.example.com"]}',?)`, []any{session, at}},
	} {
		_, err := database.Exec(statement.sql, statement.args...)
		testutil.FailErr(t, "seed released credential", err)
	}
}

// The upgrade path never touches device credentials, user settings, owner
// identity or conversation history; these tables must hold their released rows.
var retainedUserTables = []string{
	"people", "projects", "project_roots", "sessions", "messages", "chat_grants",
	"managed_secrets", "managed_secret_versions", "vault_unlocks", "credential_authored_values",
	"artifacts", "invocation_receipts", "checkpoints", "source_recovery_entries",
}

func TestReleasedCorpusUpgradesToCurrentAndRetainsUserData(t *testing.T) {
	current, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "current baseline", err)
	for _, version := range releasedCorpusStores(t) {
		t.Run(version, func(t *testing.T) {
			path := copyReleasedStore(t, version)
			raw, err := os.ReadFile(filepath.Join(releasedCorpusRoot, version, "SEMANTICS.json"))
			testutil.FailErr(t, "read corpus semantics", err)
			var semantics corpusSemantics
			testutil.FailErr(t, "decode corpus semantics", json.Unmarshal(raw, &semantics))

			source, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "open released store", err)
			seedReleasedCredentials(t, source)
			released := tableColumns(t, source)
			for _, table := range retainedUserTables {
				if _, ok := released[table]; !ok {
					t.Fatalf("released store lacks retained table %s", table)
				}
			}
			for _, table := range retainedUserTables {
				if len(projectedRows(t, source, table, released[table])) == 0 && !slices.Contains([]string{"checkpoints", "source_recovery_entries"}, table) {
					t.Fatalf("released store has no %s rows to retain", table)
				}
			}
			before := map[string][]string{}
			for table, columns := range released {
				before[table] = projectedRows(t, source, table, retainedColumns(table, columns))
			}
			testutil.FailErr(t, "close released store", source.Close())

			for boot := range 2 {
				testutil.FailErr(t, "upgrade released store", UpgradeStaged(t.Context(), path))
				reader, err := openReader(t.Context(), path)
				testutil.FailErr(t, "open upgraded store", err)
				identity, err := inspectSchema(t.Context(), reader)
				testutil.FailErr(t, "inspect upgraded store", err)
				if identity != current {
					t.Fatalf("boot %d: upgraded identity %+v, want %+v", boot, identity, current)
				}
				upgraded := tableColumns(t, reader)
				for table, columns := range released {
					after, ok := upgraded[table]
					if successor, retired := retiredByRegisteredSteps[table]; retired {
						if ok {
							t.Fatalf("retired table %s survived upgrade", table)
						}
						if successor == "" {
							continue
						}
						if got := len(projectedRows(t, reader, successor, upgraded[successor])); got < len(before[table]) {
							t.Fatalf("%s carries %d rows of %d retired %s rows", successor, got, len(before[table]), table)
						}
						continue
					}
					if !ok {
						t.Fatalf("upgrade dropped released table %s", table)
					}
					if sqliteShadowTable(table) {
						continue
					}
					if table == "schema_migrations" {
						ledger := projectedRows(t, reader, table, columns)
						for _, row := range before[table] {
							if !slices.Contains(ledger, row) {
								t.Fatalf("upgrade rewrote released migration record %s", row)
							}
						}
						continue
					}
					if _, reshaped := reshapedByRegisteredSteps[table]; reshaped {
						if got := len(projectedRows(t, reader, table, after)); got != len(before[table]) {
							t.Fatalf("%s: reshaped table has %d rows, released %d", table, got, len(before[table]))
						}
						continue
					}
					shared := sharedColumns(columns, after)
					if retained := retainedColumns(table, columns); !slices.Equal(shared, retained) {
						t.Fatalf("%s: upgrade removed released columns %v -> %v", table, columns, after)
					}
					if got := projectedRows(t, reader, table, shared); !slices.Equal(got, before[table]) {
						t.Fatalf("%s: released rows changed\nbefore %v\nafter  %v", table, before[table], got)
					}
				}
				for table := range upgraded {
					if strings.HasPrefix(table, "invocation_program") {
						var count int
						testutil.FailErr(t, "count "+table, reader.QueryRow(`SELECT count(*) FROM "`+table+`"`).Scan(&count))
						if count != 0 {
							t.Fatalf("%s: upgrade synthesized %d program rows", table, count)
						}
					}
				}
				assertCorpusSemantics(t, reader, semantics)
				testutil.FailErr(t, "close upgraded store", reader.Close())
			}
		})
	}
}

func assertCorpusSemantics(t *testing.T, reader *sql.DB, semantics corpusSemantics) {
	t.Helper()
	for _, message := range semantics.Messages {
		var role, content string
		testutil.FailErr(t, "read message "+message.ID, reader.QueryRow(`SELECT role, content FROM messages WHERE id=?`, message.ID).Scan(&role, &content))
		if role != message.Role || (message.Content != "" && content != message.Content) {
			t.Fatalf("message %s became %s %q", message.ID, role, content)
		}
	}
	if semantics.Artifact.ID != "" {
		var count int
		testutil.FailErr(t, "read artifact", reader.QueryRow(`SELECT count(*) FROM artifacts WHERE id=?`, semantics.Artifact.ID).Scan(&count))
		if count != 1 {
			t.Fatalf("artifact %s missing after upgrade", semantics.Artifact.ID)
		}
	}
	var owners int
	testutil.FailErr(t, "count owners", reader.QueryRow(`SELECT count(*) FROM people`).Scan(&owners))
	if owners == 0 {
		t.Fatal("upgrade lost the owner identity")
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read store bytes", err)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// durableSiblings lists files beside the store that hold content; an empty
// write-ahead log or shared-memory index left by a read-only probe holds none.
func durableSiblings(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "list store directory", err)
	var names []string
	for _, entry := range entries {
		info, err := entry.Info()
		testutil.FailErr(t, "stat store sibling", err)
		if strings.HasSuffix(entry.Name(), "-shm") || (strings.HasSuffix(entry.Name(), "-wal") && info.Size() == 0) {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

func TestUnknownStoreShapesRefuseWithoutMutation(t *testing.T) {
	newest := releasedCorpusStores(t)
	latest := newest[len(newest)-1]
	cases := []struct {
		name    string
		current bool
		alter   string
	}{
		{name: "released revision with unregistered table", alter: `CREATE TABLE future_shape(id TEXT PRIMARY KEY)`},
		{name: "released revision with unregistered column", alter: `ALTER TABLE sessions ADD COLUMN future_column TEXT`},
		{name: "future revision", alter: `PRAGMA user_version=99`},
		{name: "current revision with unregistered program column", current: true, alter: `ALTER TABLE invocation_programs ADD COLUMN future_column TEXT`},
		{name: "current revision with unregistered table", current: true, alter: `CREATE TABLE future_shape(id TEXT PRIMARY KEY)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := copyReleasedStore(t, latest)
			if tc.current {
				testutil.FailErr(t, "upgrade to current", UpgradeStaged(t.Context(), path))
			}
			database, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "open store", err)
			_, err = database.Exec(tc.alter)
			testutil.FailErr(t, "write unknown shape", err)
			_, err = database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
			testutil.FailErr(t, "checkpoint unknown shape", err)
			testutil.FailErr(t, "close store", database.Close())
			digest, names := fileDigest(t, path), durableSiblings(t, filepath.Dir(path))

			err = UpgradeStaged(t.Context(), path)
			if err == nil {
				t.Fatal("unknown shape was accepted")
			}
			var incompatible *StoreIncompatibleError
			if !errors.Is(err, migrations.ErrUnsupported) && !errors.As(err, &incompatible) {
				t.Fatalf("unknown shape refused with %v", err)
			}
			if got := fileDigest(t, path); got != digest {
				t.Fatal("refused upgrade mutated the store")
			}
			if got := durableSiblings(t, filepath.Dir(path)); !slices.Equal(got, names) {
				t.Fatalf("refused upgrade left files %v, had %v", got, names)
			}
		})
	}
}
