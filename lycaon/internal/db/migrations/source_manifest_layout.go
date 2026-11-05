package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

//go:embed source_manifest_layout.go
var manifestLayoutImplementation []byte

//go:embed source_manifest_layout.sql
var manifestLayoutSchema []byte

//go:embed source_manifest_converter.go
var manifestLayoutConverterImplementation []byte

//go:embed source_manifest_memo.go
var manifestLayoutMemoImplementation []byte

// The chunking rule and identities below are the source snapshot store's,
// so a converted manifest is the one a capture of the same tree builds.
const (
	layoutChunkMinEntries = 128
	layoutChunkMaxEntries = 4096
	layoutChunkCutMask    = 511
	layoutInsertRows      = 64
)

type layoutEntry struct {
	path, sha256, gitOID, identity string
	size, mode, modifiedNS         int64
}

type layoutChunk struct {
	firstPath, id, contentID    string
	entries, totalBytes, hashed int64
}

func migrateManifestLayout(ctx context.Context, tx *sql.Tx) error {
	// Create target tables and indexes before inserting; a later bulk sort over
	// amplified rows would need a different retained-storage reservation.
	if _, err := tx.ExecContext(ctx, string(manifestLayoutSchema)); err != nil {
		return err
	}
	if err := convertLegacyRoots(ctx, tx); err != nil {
		return err
	}
	// Children drop before their parent so no drop deletes row by row.
	for _, table := range []string{
		"legacy_source_manifest_entries", "legacy_source_snapshot_chunks",
		"legacy_source_snapshot_roots", "legacy_source_manifest_chunks",
	} {
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+table); err != nil { // #nosec G202 -- fixed table names
			return err
		}
	}
	return nil
}

type legacyRoot struct {
	snapshotID, path string
	ordinal          int64
}

func convertLegacyRoots(ctx context.Context, tx *sql.Tx) error {
	roots, err := legacySnapshotRoots(ctx, tx)
	if err != nil || len(roots) == 0 {
		return err
	}
	snapshotChunks, err := legacySnapshotChunks(ctx, tx)
	if err != nil {
		return err
	}
	insert, err := prepareLayoutEntryInsert(ctx, tx, layoutInsertRows)
	if err != nil {
		return err
	}
	defer func() { _ = insert.Close() }()
	readChunk, err := tx.PrepareContext(ctx, `SELECT root_path, path, sha256, git_oid, identity, size, mode, modified_ns
FROM legacy_source_manifest_entries WHERE chunk_id = ?`)
	if err != nil {
		return err
	}
	defer func() { _ = readChunk.Close() }()
	converter := newLegacyManifestConverter(tx, insert, readChunk)
	for _, root := range roots {
		manifest, err := converter.convertRoot(ctx, root.path, snapshotChunks[root.snapshotID])
		if err != nil {
			return fmt.Errorf("convert snapshot %s root %s: %w", root.snapshotID, root.path, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_snapshot_roots (snapshot_id, root_path, ordinal, manifest_id)
VALUES (?, ?, ?, ?)`, root.snapshotID, root.path, root.ordinal, manifest); err != nil {
			return err
		}
	}
	return nil
}

func legacySnapshotRoots(ctx context.Context, tx *sql.Tx) ([]legacyRoot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT snapshot_id, root_path, ordinal FROM legacy_source_snapshot_roots
ORDER BY snapshot_id, ordinal`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []legacyRoot
	for rows.Next() {
		var root legacyRoot
		if err := rows.Scan(&root.snapshotID, &root.path, &root.ordinal); err != nil {
			return nil, err
		}
		out = append(out, root)
	}
	return out, rows.Err()
}

func legacySnapshotChunks(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT snapshot_id, chunk_id FROM legacy_source_snapshot_chunks
ORDER BY snapshot_id, chunk_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var snapshotID, chunkID string
		if err := rows.Scan(&snapshotID, &chunkID); err != nil {
			return nil, err
		}
		chunks := out[snapshotID]
		if len(chunks) == 0 || chunks[len(chunks)-1] != chunkID {
			out[snapshotID] = append(chunks, chunkID)
		}
	}
	return out, rows.Err()
}

func layoutCutAfter(path string, n int) bool {
	if n >= layoutChunkMaxEntries {
		return true
	}
	if n < layoutChunkMinEntries {
		return false
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	return h.Sum64()&layoutChunkCutMask == 0
}

func writeLayoutChunk(ctx context.Context, tx *sql.Tx, insert *sql.Stmt, entries []layoutEntry) (layoutChunk, error) {
	return insertLayoutChunk(ctx, tx, insert, buildLayoutChunk(entries), entries)
}

func buildLayoutChunk(entries []layoutEntry) layoutChunk {
	hash := sha256.New()
	_, _ = hash.Write([]byte("painted-wolf-source-manifest-chunk-v3\x00"))
	chunk := layoutChunk{firstPath: entries[0].path, entries: int64(len(entries))}
	for _, entry := range entries {
		_, _ = hash.Write([]byte(entry.path + "\x00" + layoutContentKey(entry) + "\x00"))
		_, _ = hash.Write([]byte(strconv.FormatInt(entry.size, 10) + "\x00" + strconv.FormatUint(uint64(uint32(entry.mode)), 10) + "\x00")) // #nosec G115
		chunk.totalBytes += entry.size
		if entry.identity == "hashed" {
			chunk.hashed++
		}
	}
	chunk.contentID = hex.EncodeToString(hash.Sum(nil))
	hash.Reset()
	_, _ = hash.Write([]byte("painted-wolf-source-stored-chunk-v1\x00" + chunk.contentID + "\x00"))
	for _, entry := range entries {
		_, _ = hash.Write([]byte(entry.sha256 + "\x00" + entry.gitOID + "\x00" + entry.identity + "\x00" + strconv.FormatInt(entry.modifiedNS, 10) + "\x00"))
	}
	chunk.id = hex.EncodeToString(hash.Sum(nil))
	return chunk
}

func insertLayoutChunk(ctx context.Context, tx *sql.Tx, insert *sql.Stmt, chunk layoutChunk, entries []layoutEntry) (layoutChunk, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO source_manifest_chunks (id, content_id, entry_count, total_bytes, hashed_count)
VALUES (?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, chunk.id, chunk.contentID, chunk.entries, chunk.totalBytes, chunk.hashed)
	if err != nil {
		return layoutChunk{}, err
	}
	if inserted, err := result.RowsAffected(); err != nil || inserted == 0 {
		return chunk, err
	}
	if err := insertLayoutEntries(ctx, tx, insert, chunk.id, entries); err != nil {
		return layoutChunk{}, err
	}
	return chunk, nil
}

func prepareLayoutEntryInsert(ctx context.Context, tx *sql.Tx, count int) (*sql.Stmt, error) {
	if count < 1 || count > layoutInsertRows {
		return nil, fmt.Errorf("manifest insert rows %d outside 1..%d", count, layoutInsertRows)
	}
	query := `INSERT INTO source_manifest_entries
(chunk_id, path, sha256, git_oid, identity, size, mode, modified_ns) VALUES ` + strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?,?,?),", count), ",")
	return tx.PrepareContext(ctx, query)
}

func insertLayoutEntries(ctx context.Context, tx *sql.Tx, full *sql.Stmt, chunkID string, entries []layoutEntry) error {
	var tail *sql.Stmt
	if count := len(entries) % layoutInsertRows; count != 0 {
		var err error
		tail, err = prepareLayoutEntryInsert(ctx, tx, count)
		if err != nil {
			return err
		}
		defer func() { _ = tail.Close() }()
	}
	args := make([]any, 0, layoutInsertRows*8)
	for start := 0; start < len(entries); start += layoutInsertRows {
		batch := entries[start:min(start+layoutInsertRows, len(entries))]
		statement := full
		if len(batch) < layoutInsertRows {
			statement = tail
		}
		args = args[:0]
		for _, entry := range batch {
			args = append(args, chunkID, entry.path, entry.sha256, entry.gitOID, entry.identity, entry.size, entry.mode, entry.modifiedNS)
		}
		if _, err := statement.ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

// layoutContentKey is the git id, else the digest, else the stat facts.
func layoutContentKey(entry layoutEntry) string {
	switch {
	case entry.gitOID != "":
		return "git:" + entry.gitOID
	case entry.sha256 != "":
		return "sha256:" + entry.sha256
	}
	return "stat:" + strconv.FormatInt(entry.size, 10) + ":" + strconv.FormatUint(uint64(uint32(entry.mode)), 10) + ":" + strconv.FormatInt(entry.modifiedNS, 10) // #nosec G115
}

func writeLayoutManifest(ctx context.Context, tx *sql.Tx, chunks []layoutChunk) (string, error) {
	hash := sha256.New()
	_, _ = hash.Write([]byte("painted-wolf-source-manifest-v1\x00"))
	var entries, totalBytes int64
	for _, chunk := range chunks {
		_, _ = hash.Write([]byte(chunk.id + "\x00"))
		entries += chunk.entries
		totalBytes += chunk.totalBytes
	}
	id := hex.EncodeToString(hash.Sum(nil))
	result, err := tx.ExecContext(ctx, `INSERT INTO source_manifests (id, entry_count, total_bytes)
VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`, id, entries, totalBytes)
	if err != nil {
		return "", err
	}
	if inserted, err := result.RowsAffected(); err != nil || inserted == 0 {
		return id, err
	}
	for _, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_manifest_layout (manifest_id, first_path, chunk_id)
VALUES (?, ?, ?)`, id, chunk.firstPath, chunk.id); err != nil {
			return "", err
		}
	}
	return id, nil
}
