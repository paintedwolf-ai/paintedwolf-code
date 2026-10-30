package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"modernc.org/sqlite"
)

type manifestInsertTrace struct {
	prepares, live, maximum int
	arguments               []int
	entrySQL                string
	afterExec               func()
}

type manifestTraceDriver struct{ trace *manifestInsertTrace }
type manifestTraceConn struct {
	driver.Conn
	trace *manifestInsertTrace
}
type manifestTraceStmt struct {
	driver.Stmt
	trace *manifestInsertTrace
	entry bool
}

var manifestTraceSequence atomic.Uint64

func (d manifestTraceDriver) Open(name string) (driver.Conn, error) {
	connection, err := (&sqlite.Driver{}).Open(name)
	return &manifestTraceConn{Conn: connection, trace: d.trace}, err
}

func (c *manifestTraceConn) Prepare(query string) (driver.Stmt, error) {
	statement, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	entry := strings.HasPrefix(strings.TrimSpace(query), "INSERT INTO source_manifest_entries")
	if entry {
		c.trace.prepares++
		c.trace.live++
		c.trace.maximum = max(c.trace.maximum, c.trace.live)
		c.trace.entrySQL = query
	}
	return &manifestTraceStmt{Stmt: statement, trace: c.trace, entry: entry}, nil
}

func (s *manifestTraceStmt) Close() error {
	if s.entry {
		s.trace.live--
	}
	return s.Stmt.Close()
}

func (s *manifestTraceStmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, len(args))
	for index, value := range args {
		named[index] = driver.NamedValue{Ordinal: index + 1, Value: value}
	}
	return s.ExecContext(context.Background(), named)
}

func (s *manifestTraceStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.entry {
		s.trace.arguments = append(s.trace.arguments, len(args))
	}
	result, err := s.Stmt.(driver.StmtExecContext).ExecContext(ctx, args)
	if s.entry && err == nil && s.trace.afterExec != nil {
		s.trace.afterExec()
	}
	return result, err
}

func newManifestBatchDatabase(t *testing.T) (*sql.DB, *manifestInsertTrace) {
	t.Helper()
	trace := &manifestInsertTrace{}
	name := fmt.Sprintf("manifest-trace-%d", manifestTraceSequence.Add(1))
	sql.Register(name, manifestTraceDriver{trace})
	database, err := sql.Open(name, filepath.Join(t.TempDir(), "fixture.db"))
	testutil.FailErr(t, "open actual SQLite migration writer", err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE source_snapshots(id TEXT PRIMARY KEY);
CREATE TABLE history_storage_clock(id INTEGER PRIMARY KEY,generation INTEGER);
INSERT INTO history_storage_clock VALUES(1,0);
CREATE TABLE source_blob_objects(sha256 TEXT PRIMARY KEY);
CREATE TABLE source_blob_reclaim_queue(sha256 TEXT PRIMARY KEY);`)
	testutil.FailErr(t, "create target dependencies", err)
	// The prefix retires legacy tables. Use the actual migration's complete
	// target DDL, including its foreign keys, indexes and triggers.
	start := strings.Index(string(manifestLayoutSchema), "CREATE TABLE IF NOT EXISTS source_manifests (")
	if start < 0 {
		t.Fatal("manifest target DDL not found")
	}
	_, err = database.Exec(string(manifestLayoutSchema[start:]))
	testutil.FailErr(t, "create actual manifest target", err)
	return database, trace
}

func manifestBatchEntries(count int) []layoutEntry {
	entries := make([]layoutEntry, count)
	for index := range entries {
		entries[index] = layoutEntry{path: fmt.Sprintf("dir/file-%05d-'λ'.go", index), sha256: fmt.Sprintf("%064x", index+1), identity: "hashed", size: int64(index + 1), mode: 420, modifiedNS: int64(9000000000 + index)}
	}
	return entries
}

func TestManifestMigrationBatchBoundariesPreserveRowsAndBoundCalls(t *testing.T) {
	for _, count := range []int{1, 63, 64, 65, 127, 128, 129, 4095, 4096} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			database, trace := newManifestBatchDatabase(t)
			tx, err := database.BeginTx(t.Context(), nil)
			testutil.FailErr(t, "begin actual chunk write", err)
			defer tx.Rollback()
			insert, err := prepareLayoutEntryInsert(t.Context(), tx, 64)
			testutil.FailErr(t, "prepare full conversion statement", err)
			defer insert.Close()
			entries := manifestBatchEntries(count)
			chunk, err := writeLayoutChunk(t.Context(), tx, insert, entries)
			testutil.FailErr(t, "write actual chunk", err)
			if len(trace.arguments) != (count+63)/64 || trace.maximum > 2 || trace.prepares != 1+min(count%64, 1) {
				t.Fatalf("unbounded insert work: prepares=%d live=%d calls=%d rows=%d", trace.prepares, trace.maximum, len(trace.arguments), count)
			}
			for _, arguments := range trace.arguments {
				if arguments < 8 || arguments > 512 || arguments%8 != 0 {
					t.Fatalf("insert argument bound violated: %d", arguments)
				}
			}
			rows, err := tx.Query(`SELECT path,sha256,git_oid,identity,size,mode,modified_ns FROM source_manifest_entries WHERE chunk_id=? ORDER BY path`, chunk.id)
			testutil.FailErr(t, "read persisted chunk", err)
			defer rows.Close()
			var got []layoutEntry
			for rows.Next() {
				var entry layoutEntry
				testutil.FailErr(t, "read complete source identity", rows.Scan(&entry.path, &entry.sha256, &entry.gitOID, &entry.identity, &entry.size, &entry.mode, &entry.modifiedNS))
				got = append(got, entry)
			}
			testutil.FailErr(t, "read all chunk entries", rows.Err())
			testutil.FailErr(t, "close entry cursor", rows.Close())
			if !reflect.DeepEqual(entries, got) {
				t.Fatal("batching changed source paths, bytes or observation identity")
			}
			calls, prepares := len(trace.arguments), trace.prepares
			reused, err := writeLayoutChunk(t.Context(), tx, insert, entries)
			testutil.FailErr(t, "reuse identical stored chunk", err)
			if reused != chunk || calls != len(trace.arguments) || prepares != trace.prepares {
				t.Fatal("deduplicated chunk repeated entry writes or changed identity")
			}
		})
	}
}

func TestManifestMigrationBatchFailureAndCancellationRollBack(t *testing.T) {
	for _, cancellation := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancellation=%t", cancellation), func(t *testing.T) {
			database, trace := newManifestBatchDatabase(t)
			before := manifestBatchState(t, database)
			tx, err := database.BeginTx(t.Context(), nil)
			testutil.FailErr(t, "begin interrupted conversion", err)
			insert, err := prepareLayoutEntryInsert(t.Context(), tx, 64)
			testutil.FailErr(t, "prepare interrupted writer", err)
			defer insert.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entries := manifestBatchEntries(130)
			if cancellation {
				trace.afterExec = cancel
			} else {
				entries[70].size = -1
			}
			if _, err := writeLayoutChunk(ctx, tx, insert, entries); err == nil {
				t.Fatal("interrupted conversion accepted")
			}
			if len(trace.arguments) == 0 {
				t.Fatal("interruption did not exercise an already written batch")
			}
			testutil.FailErr(t, "close interrupted insert", insert.Close())
			testutil.FailErr(t, "roll back whole conversion", tx.Rollback())
			if !reflect.DeepEqual(before, manifestBatchState(t, database)) {
				t.Fatal("interrupted batching changed database schema, rows or storage clock")
			}
		})
	}
}

func manifestBatchState(t *testing.T, database *sql.DB) map[string][]string {
	t.Helper()
	state := map[string][]string{}
	for _, table := range []string{"sqlite_schema", "source_manifest_chunks", "source_manifest_entries", "history_storage_clock"} {
		state[table] = manifestBatchTableState(t, database, table)
	}
	return state
}

// Each cursor releases the sole database connection before the next table.
func manifestBatchTableState(t *testing.T, database *sql.DB, table string) []string {
	t.Helper()
	rows, err := database.Query("SELECT * FROM " + table)
	testutil.FailErr(t, "read conversion state", err)
	defer rows.Close()
	columns, err := rows.Columns()
	testutil.FailErr(t, "read conversion columns", err)
	var records []string
	for rows.Next() {
		values, destinations := make([]any, len(columns)), make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		testutil.FailErr(t, "read conversion row", rows.Scan(destinations...))
		encoded, err := json.Marshal(values)
		testutil.FailErr(t, "encode conversion row", err)
		records = append(records, string(encoded))
	}
	testutil.FailErr(t, "read complete conversion state", rows.Err())
	testutil.FailErr(t, "close conversion state cursor", rows.Close())
	sort.Strings(records)
	return records
}
