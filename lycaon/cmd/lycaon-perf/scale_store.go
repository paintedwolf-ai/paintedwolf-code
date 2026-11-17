package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/scalefixture"
)

type storeShape struct {
	pageSize, pages, free, fileBytes int64
}

func openFixtureStore(path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	return database, nil
}

func readStoreShape(ctx context.Context, path string) (storeShape, error) {
	database, err := openFixtureStore(path)
	if err != nil {
		return storeShape{}, err
	}
	defer func() { _ = database.Close() }()
	return queryStoreShape(ctx, database, path)
}

func queryStoreShape(ctx context.Context, database *sql.DB, path string) (storeShape, error) {
	var shape storeShape
	for _, read := range []struct {
		pragma string
		into   *int64
	}{{"page_size", &shape.pageSize}, {"page_count", &shape.pages}, {"freelist_count", &shape.free}} {
		if err := database.QueryRowContext(ctx, "PRAGMA "+read.pragma).Scan(read.into); err != nil {
			return shape, fmt.Errorf("read %s: %w", read.pragma, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return shape, err
	}
	shape.fileBytes = info.Size()
	return shape, nil
}

// freePageBatch bounds the write-ahead log while the freelist grows.
const freePageBatch = 65_536

// addFreePages fills a scratch table with one row per page and drops it. The
// rows are fixed, so the freelist it leaves is the same on every run.
func addFreePages(ctx context.Context, path string, pages int64) (storeShape, error) {
	database, err := openFixtureStore(path)
	if err != nil {
		return storeShape{}, err
	}
	defer func() { _ = database.Close() }()
	var pageSize int64
	if err := database.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return storeShape{}, err
	}
	if _, err := database.ExecContext(ctx, "CREATE TABLE scale_free_pages (body BLOB NOT NULL)"); err != nil {
		return storeShape{}, err
	}
	// One row fills one leaf page, below the largest payload a leaf holds.
	rowBytes := pageSize - 128
	for inserted := int64(0); inserted < pages; inserted += freePageBatch {
		rows := min(freePageBatch, pages-inserted)
		if _, err := database.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
INSERT INTO scale_free_pages (body) SELECT zeroblob(?) FROM n`, rows, rowBytes); err != nil {
			return storeShape{}, err
		}
		if _, err := database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return storeShape{}, err
		}
	}
	if _, err := database.ExecContext(ctx, "DROP TABLE scale_free_pages"); err != nil {
		return storeShape{}, err
	}
	if _, err := database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return storeShape{}, err
	}
	return queryStoreShape(ctx, database, path)
}

type foreignKey struct {
	parent   string
	from, to []string
	cascades bool
}

// countOwnedRows counts the project's row and every row that a cascading
// foreign key chain ties to it: what deleting the project row would delete.
func countOwnedRows(ctx context.Context, path, projectID string) (int64, error) {
	database, err := openFixtureStore(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = database.Close() }()
	keys, err := cascadeKeys(ctx, database)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, table := range slices.Sorted(maps.Keys(keys)) {
		predicate := ownedPredicate(keys, table, map[string]bool{})
		if predicate == "" {
			continue
		}
		var count int64
		query := "SELECT count(*) FROM " + quoteName(table) + " WHERE " + predicate // #nosec G202 -- names read from the schema
		if err := database.QueryRowContext(ctx, query, projectID).Scan(&count); err != nil {
			return 0, fmt.Errorf("count rows %s owns in %s: %w", projectID, table, err)
		}
		total += count
	}
	return total, nil
}

func cascadeKeys(ctx context.Context, database *sql.DB) (map[string][]foreignKey, error) {
	rows, err := database.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	keys := make(map[string][]foreignKey, len(tables))
	for _, table := range tables {
		keys[table] = nil
		list, err := database.QueryContext(ctx, `SELECT id, "table", "from", coalesce("to", ''), on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
		if err != nil {
			return nil, err
		}
		byID := map[int]*foreignKey{}
		var order []int
		for list.Next() {
			var id int
			var parent, from, to, onDelete string
			if err := list.Scan(&id, &parent, &from, &to, &onDelete); err != nil {
				_ = list.Close()
				return nil, err
			}
			key, ok := byID[id]
			if !ok {
				key = &foreignKey{parent: parent, cascades: strings.EqualFold(onDelete, "CASCADE")}
				byID[id] = key
				order = append(order, id)
			}
			key.from, key.to = append(key.from, from), append(key.to, to)
		}
		if err := errors.Join(list.Err(), list.Close()); err != nil {
			return nil, err
		}
		for _, id := range order {
			if byID[id].cascades {
				keys[table] = append(keys[table], *byID[id])
			}
		}
	}
	return keys, nil
}

// ownedPredicate selects table's rows owned by the project bound as ?1.
func ownedPredicate(keys map[string][]foreignKey, table string, visiting map[string]bool) string {
	if table == "projects" {
		return "id = ?1"
	}
	if visiting[table] {
		return ""
	}
	visiting[table] = true
	defer delete(visiting, table)
	var terms []string
	for _, key := range keys[table] {
		parent := ownedPredicate(keys, key.parent, visiting)
		if parent == "" {
			continue
		}
		to := key.to
		if to[0] == "" {
			to = []string{"rowid"}
		}
		terms = append(terms, "("+quoteNames(key.from)+") IN (SELECT "+quoteNames(to)+" FROM "+quoteName(key.parent)+" WHERE "+parent+")")
	}
	return strings.Join(terms, " OR ")
}

func quoteName(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func quoteNames(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quoteName(name)
	}
	return strings.Join(quoted, ", ")
}

// releasedRoot is where the seeded inventory says its project was.
const releasedRoot = "/scale-fixture/project"

type releasedEntry struct {
	path string
	size int64
}

// seedReleasedInventory adds to a released store one complete inventory of
// files entries in that release's layout: 256 buckets keyed by the first byte
// of each path's digest, one chunk per bucket.
func seedReleasedInventory(ctx context.Context, path string, files int) error {
	var buckets [256][]releasedEntry
	var totalBytes int64
	for index := range files {
		rel := scalefixture.Path(index)
		bucket := sha256.Sum256([]byte(releasedRoot + "\x00" + rel))
		size := int64(len(scalefixture.Body(index)))
		buckets[bucket[0]] = append(buckets[bucket[0]], releasedEntry{path: rel, size: size})
		totalBytes += size
	}
	database, err := openFixtureStore(path)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	insertEntry, err := tx.PrepareContext(ctx, `INSERT INTO source_manifest_entries
(chunk_id, ordinal, root_path, path, sha256, git_oid, identity, size, mode, modified_ns) VALUES (?, ?, ?, ?, '', '', 'stat', ?, 420, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = insertEntry.Close() }()
	snapshot := sha256.New()
	_, _ = snapshot.Write([]byte("scale-fixture-snapshot\x00" + releasedRoot + "\x00"))
	type chunkRef struct {
		bucket int
		id     string
	}
	var chunks []chunkRef
	for bucket, entries := range buckets {
		if len(entries) == 0 {
			continue
		}
		slices.SortFunc(entries, func(a, b releasedEntry) int { return strings.Compare(a.path, b.path) })
		digest := sha256.New()
		_, _ = digest.Write([]byte("scale-fixture-chunk\x00" + strconv.Itoa(bucket)))
		var chunkBytes int64
		for _, entry := range entries {
			_, _ = digest.Write([]byte(entry.path + "\x00" + strconv.FormatInt(entry.size, 10) + "\x00"))
			chunkBytes += entry.size
		}
		id := hex.EncodeToString(digest.Sum(nil))
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_manifest_chunks (id, entry_count, total_bytes) VALUES (?, ?, ?)`,
			id, len(entries), chunkBytes); err != nil {
			return err
		}
		for ordinal, entry := range entries {
			if _, err := insertEntry.ExecContext(ctx, id, ordinal, releasedRoot, entry.path, entry.size, int64(ordinal)+1_700_000_000_000_000_000); err != nil {
				return err
			}
		}
		_, _ = snapshot.Write([]byte{byte(bucket)})
		_, _ = snapshot.Write([]byte(id + "\x00"))
		chunks = append(chunks, chunkRef{bucket: bucket, id: id})
	}
	snapshotID := hex.EncodeToString(snapshot.Sum(nil))
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_snapshots (id, roots_key, merkle_sha256, file_count, total_bytes, capture_quality, created_ts)
VALUES (?, ?, ?, ?, ?, 'exact', '2026-10-20T00:00:00Z')`, snapshotID, releasedRoot, snapshotID, files, totalBytes); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_snapshot_roots (snapshot_id, root_path, ordinal) VALUES (?, ?, 0)`, snapshotID, releasedRoot); err != nil {
		return err
	}
	for _, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_snapshot_chunks (snapshot_id, bucket, chunk_id) VALUES (?, ?, ?)`,
			snapshotID, chunk.bucket, chunk.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}
