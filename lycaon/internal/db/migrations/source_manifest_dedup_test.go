package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"modernc.org/sqlite"
)

type manifestMigrationTrace struct {
	chunkReads          []string
	manifestInserts     int
	layoutInserts       int
	chunkInserts        int
	entryInsertBatches  int
	rootInserts         int
	createdIndexes      []string
	insertsAfterIndexes int
}

type manifestMigrationTraceDriver struct{ trace *manifestMigrationTrace }

type manifestMigrationTraceConn struct {
	driver.Conn
	trace *manifestMigrationTrace
}

type manifestMigrationTraceStmt struct {
	driver.Stmt
	trace *manifestMigrationTrace
	query string
}

func (d manifestMigrationTraceDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(name)
	return &manifestMigrationTraceConn{Conn: conn, trace: d.trace}, err
}

func (c *manifestMigrationTraceConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &manifestMigrationTraceStmt{Stmt: stmt, trace: c.trace, query: strings.TrimSpace(query)}, nil
}

func (c *manifestMigrationTraceConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.trace.recordExec(query)
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	stmt, err := c.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stmt.Close() }()
	return stmt.(driver.StmtExecContext).ExecContext(ctx, args)
}

func (s *manifestMigrationTraceStmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.ExecContext(context.Background(), named)
}

func (s *manifestMigrationTraceStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	s.trace.recordExec(s.query)
	return s.Stmt.(driver.StmtExecContext).ExecContext(ctx, args)
}

func (s *manifestMigrationTraceStmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.QueryContext(context.Background(), named)
}

func (s *manifestMigrationTraceStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(s.query, "FROM legacy_source_manifest_entries WHERE chunk_id = ?") && len(args) == 1 {
		if chunkID, ok := args[0].Value.(string); ok {
			s.trace.chunkReads = append(s.trace.chunkReads, chunkID)
		}
	}
	return s.Stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
}

func (t *manifestMigrationTrace) recordExec(query string) {
	trimmed := strings.TrimSpace(query)
	for _, indexName := range []string{
		"idx_source_snapshot_roots_manifest",
		"idx_source_manifest_layout_chunk",
		"idx_source_manifest_entries_sha",
	} {
		if strings.Contains(trimmed, "CREATE INDEX IF NOT EXISTS "+indexName) {
			t.createdIndexes = append(t.createdIndexes, indexName)
		}
	}
	isBulkInsert := false
	switch {
	case strings.HasPrefix(trimmed, "INSERT INTO source_manifests "):
		t.manifestInserts++
		isBulkInsert = true
	case strings.HasPrefix(trimmed, "INSERT INTO source_manifest_layout "):
		t.layoutInserts++
		isBulkInsert = true
	case strings.HasPrefix(trimmed, "INSERT INTO source_manifest_chunks "):
		t.chunkInserts++
		isBulkInsert = true
	case strings.HasPrefix(trimmed, "INSERT INTO source_manifest_entries"):
		t.entryInsertBatches++
		isBulkInsert = true
	case strings.HasPrefix(trimmed, "INSERT INTO source_snapshot_roots "):
		t.rootInserts++
		isBulkInsert = true
	}
	if isBulkInsert && len(t.createdIndexes) > 0 {
		t.insertsAfterIndexes++
	}
}

func newLegacyMigrationDatabase(t *testing.T) (*sql.DB, *manifestMigrationTrace) {
	t.Helper()
	trace := &manifestMigrationTrace{}
	name := fmt.Sprintf("manifest-migration-trace-%d", manifestTraceSequence.Add(1))
	sql.Register(name, manifestMigrationTraceDriver{trace: trace})
	database, err := sql.Open(name, filepath.Join(t.TempDir(), "legacy.db"))
	testutil.FailErr(t, "open traced legacy database", err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE history_storage_clock(id INTEGER PRIMARY KEY, generation INTEGER);
INSERT INTO history_storage_clock VALUES(1, 0);
CREATE TABLE source_blob_objects(sha256 TEXT PRIMARY KEY);
CREATE TABLE source_blob_reclaim_queue(sha256 TEXT PRIMARY KEY);
CREATE TABLE source_snapshots (
    id TEXT PRIMARY KEY,
    roots_key TEXT NOT NULL,
    merkle_sha256 TEXT NOT NULL,
    file_count INTEGER NOT NULL CHECK (file_count >= 0),
    total_bytes INTEGER NOT NULL CHECK (total_bytes >= 0),
    capture_quality TEXT NOT NULL DEFAULT 'exact' CHECK (capture_quality IN ('exact', 'observed')),
    created_ts TEXT NOT NULL
) STRICT;
CREATE TABLE source_snapshot_roots (
    snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (snapshot_id, root_path)
) STRICT, WITHOUT ROWID;
CREATE TABLE source_manifest_chunks (
    id TEXT PRIMARY KEY,
    entry_count INTEGER NOT NULL CHECK (entry_count > 0),
    total_bytes INTEGER NOT NULL CHECK (total_bytes >= 0)
) STRICT;
CREATE TABLE source_snapshot_chunks (
    snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id) ON DELETE CASCADE,
    bucket INTEGER NOT NULL CHECK (bucket >= 0 AND bucket < 256),
    chunk_id TEXT NOT NULL REFERENCES source_manifest_chunks(id),
    PRIMARY KEY (snapshot_id, bucket),
    UNIQUE (snapshot_id, chunk_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_source_snapshot_chunks_chunk ON source_snapshot_chunks(chunk_id);
CREATE TABLE source_manifest_entries (
    chunk_id TEXT NOT NULL REFERENCES source_manifest_chunks(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    root_path TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    git_oid TEXT NOT NULL DEFAULT '',
    identity TEXT NOT NULL CHECK (identity IN ('index', 'hashed', 'stat')),
    size INTEGER NOT NULL CHECK (size >= 0),
    mode INTEGER NOT NULL,
    modified_ns INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chunk_id, ordinal),
    UNIQUE (chunk_id, root_path, path)
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_source_manifest_entries_path ON source_manifest_entries(root_path, path);
CREATE INDEX idx_source_manifest_entries_hashed ON source_manifest_entries(chunk_id, root_path, path) WHERE identity = 'hashed';
CREATE INDEX idx_source_manifest_entries_sha ON source_manifest_entries(sha256);
CREATE TABLE source_manifest_staging (
    build_id TEXT NOT NULL,
    bucket INTEGER NOT NULL CHECK (bucket >= 0 AND bucket < 256),
    root_path TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    git_oid TEXT NOT NULL DEFAULT '',
    identity TEXT NOT NULL CHECK (identity IN ('index', 'hashed', 'stat')),
    size INTEGER NOT NULL CHECK (size >= 0),
    mode INTEGER NOT NULL,
    modified_ns INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (build_id, bucket, root_path, path)
) STRICT, WITHOUT ROWID;
CREATE TRIGGER history_clock_source_manifest_entries_insert AFTER INSERT ON source_manifest_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER history_clock_source_manifest_entries_update AFTER UPDATE ON source_manifest_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER history_clock_source_manifest_entries_delete AFTER DELETE ON source_manifest_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER queue_deleted_manifest_source_blob AFTER DELETE ON source_manifest_entries
WHEN OLD.sha256 != '' BEGIN
 INSERT OR IGNORE INTO source_blob_reclaim_queue(sha256) SELECT OLD.sha256 WHERE EXISTS(SELECT 1 FROM source_blob_objects WHERE sha256=OLD.sha256);
END;`)
	testutil.FailErr(t, "create legacy manifest schema", err)
	return database, trace
}

func TestManifestMigrationDeduplicatesSharedChunksAndDefersSecondaryIndexes(t *testing.T) {
	database, trace := newLegacyMigrationDatabase(t)
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := database.Exec(query, args...)
		testutil.FailErr(t, "seed legacy fixture", err)
	}
	for _, chunkID := range []string{"shared-0", "shared-1", "delta-b", "delta-a", "orphan"} {
		exec(`INSERT INTO source_manifest_chunks (id, entry_count, total_bytes) VALUES (?, 1, 1)`, chunkID)
	}
	// Seed 600 entries in shared-0 for /work/a so layoutCutAfter splits at least one chunk.
	for i := range 600 {
		path := fmt.Sprintf("pkg/file-%04d.go", i)
		sha := fmt.Sprintf("%064x", i+1)
		exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, sha256, identity, size, mode, modified_ns)
VALUES ('shared-0', ?, '/work/a', ?, ?, 'hashed', 10, 420, 100)`, i, path, sha)
	}
	exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, git_oid, identity, size, mode, modified_ns)
VALUES ('shared-0', 600, '/work/b', 'b-first.go', '1111', 'index', 5, 420, 100)`)
	exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, git_oid, identity, size, mode, modified_ns)
VALUES ('shared-1', 0, '/work/a', 'a- early.go', '2222', 'index', 7, 420, 100)`)
	exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, git_oid, identity, size, mode, modified_ns)
VALUES ('shared-1', 1, '/work/b', 'b-second.go', '3333', 'index', 8, 420, 100)`)
	exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, git_oid, identity, size, mode, modified_ns)
VALUES ('delta-b', 0, '/work/b', 'b-third.go', '4444', 'index', 9, 420, 100)`)
	exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, git_oid, identity, size, mode, modified_ns)
VALUES ('delta-a', 0, '/work/a', 'zz-tail.go', '5555', 'index', 11, 420, 100)`)
	exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, git_oid, identity, size, mode, modified_ns)
VALUES ('orphan', 0, '/work/a', 'unreferenced.go', '6666', 'index', 12, 420, 100)`)

	type snapshotSpec struct {
		id        string
		roots     []string
		chunks    []string
		fileCount int
	}
	for _, spec := range []snapshotSpec{
		{id: "snap-1", roots: []string{"/work/a", "/work/b"}, chunks: []string{"shared-0", "shared-1"}, fileCount: 603},
		{id: "snap-2", roots: []string{"/work/a", "/work/b"}, chunks: []string{"shared-0", "shared-1"}, fileCount: 603},
		{id: "snap-3", roots: []string{"/work/a", "/work/b"}, chunks: []string{"shared-0", "shared-1", "delta-b"}, fileCount: 604},
		{id: "snap-4", roots: []string{"/work/a"}, chunks: []string{"shared-0", "shared-1", "delta-a"}, fileCount: 602},
	} {
		exec(`INSERT INTO source_snapshots (id, roots_key, merkle_sha256, file_count, total_bytes, capture_quality, created_ts)
VALUES (?, 'rk', ?, ?, 0, 'exact', '2026-10-20T00:00:00Z')`, spec.id, spec.id, spec.fileCount)
		for ordinal, root := range spec.roots {
			exec(`INSERT INTO source_snapshot_roots (snapshot_id, root_path, ordinal) VALUES (?, ?, ?)`, spec.id, root, ordinal)
		}
		for bucket, chunkID := range spec.chunks {
			exec(`INSERT INTO source_snapshot_chunks (snapshot_id, bucket, chunk_id) VALUES (?, ?, ?)`, spec.id, bucket, chunkID)
		}
	}

	*trace = manifestMigrationTrace{}
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin manifest migration", err)
	defer func() { _ = tx.Rollback() }()
	testutil.FailErr(t, "apply manifest layout migration", migrateManifestLayout(t.Context(), tx))
	testutil.FailErr(t, "validate manifest layout migration", validateManifestLayout(t.Context(), tx))
	testutil.FailErr(t, "commit manifest migration", tx.Commit())

	gotReads := append([]string(nil), trace.chunkReads...)
	slices.Sort(gotReads)
	wantReads := []string{"delta-a", "delta-b", "shared-0", "shared-1"}
	if !reflect.DeepEqual(gotReads, wantReads) {
		t.Fatalf("legacy chunk reads = %v, want each retained chunk read once %v", trace.chunkReads, wantReads)
	}
	if trace.manifestInserts != 4 {
		t.Fatalf("manifest inserts = %d, want 4 distinct root manifests across 7 snapshot roots", trace.manifestInserts)
	}
	if trace.rootInserts != 7 {
		t.Fatalf("snapshot root inserts = %d, want 7", trace.rootInserts)
	}
	if trace.insertsAfterIndexes != 0 {
		t.Fatalf("bulk inserts executed after secondary index creation = %d, want 0", trace.insertsAfterIndexes)
	}
	wantIndexes := []string{
		"idx_source_snapshot_roots_manifest",
		"idx_source_manifest_layout_chunk",
		"idx_source_manifest_entries_sha",
	}
	if !reflect.DeepEqual(trace.createdIndexes, wantIndexes) {
		t.Fatalf("deferred index creation order = %v, want %v", trace.createdIndexes, wantIndexes)
	}

	rows, err := database.Query(`SELECT snapshot_id, root_path, manifest_id FROM source_snapshot_roots ORDER BY snapshot_id, ordinal`)
	testutil.FailErr(t, "query migrated snapshot roots", err)
	defer func() { _ = rows.Close() }()
	manifests := map[string]string{}
	for rows.Next() {
		var snap, root, manifest string
		testutil.FailErr(t, "scan migrated snapshot root", rows.Scan(&snap, &root, &manifest))
		manifests[snap+":"+root] = manifest
	}
	testutil.FailErr(t, "finish scanning migrated roots", rows.Err())
	testutil.FailErr(t, "close migrated roots cursor", rows.Close())
	if manifests["snap-1:/work/a"] == "" || manifests["snap-1:/work/a"] != manifests["snap-2:/work/a"] || manifests["snap-1:/work/a"] != manifests["snap-3:/work/a"] {
		t.Fatalf("/work/a manifest was not reused across identical active chunk sets: %v", manifests)
	}
	if manifests["snap-1:/work/b"] == "" || manifests["snap-1:/work/b"] != manifests["snap-2:/work/b"] {
		t.Fatalf("/work/b manifest was not reused across identical chunk sets: %v", manifests)
	}
	if manifests["snap-3:/work/b"] == manifests["snap-1:/work/b"] {
		t.Fatalf("changed /work/b chunk set reused stale manifest: %v", manifests)
	}

	for _, indexName := range wantIndexes {
		var exists int
		testutil.FailErr(t, "check index "+indexName, database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name = ?`, indexName).Scan(&exists))
		if exists != 1 {
			t.Fatalf("expected index %s to exist after migration", indexName)
		}
	}
}

func TestManifestMigrationEmptyStoreCreatesDeferredIndexes(t *testing.T) {
	database, trace := newLegacyMigrationDatabase(t)
	*trace = manifestMigrationTrace{}
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin empty manifest migration", err)
	defer func() { _ = tx.Rollback() }()
	testutil.FailErr(t, "apply empty manifest migration", migrateManifestLayout(t.Context(), tx))
	testutil.FailErr(t, "validate empty manifest migration", validateManifestLayout(t.Context(), tx))
	testutil.FailErr(t, "commit empty manifest migration", tx.Commit())
	if len(trace.chunkReads) != 0 || trace.manifestInserts != 0 || len(trace.createdIndexes) != 3 {
		t.Fatalf("unexpected empty migration trace: %+v", trace)
	}
}
