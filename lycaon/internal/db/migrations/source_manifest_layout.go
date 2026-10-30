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

func retainedManifestCopy() *RetainedCopy {
	return &RetainedCopy{
		SourceShapes: []string{SourceNamespace().From.Shape, sourceNamespaceTarget.Shape, manifestLayoutSource.Shape},
		// Independent cardinalities: source chunk metadata states how many entries
		// must survive, rather than trusting the copying predicate to validate itself.
		ExpectedRows: map[string]string{
			"source_manifest_chunks":  `SELECT COUNT(DISTINCT chunk_id) FROM original.source_snapshot_chunks NOT INDEXED`,
			"source_manifest_entries": `SELECT COALESCE(SUM(entry_count),0) FROM original.source_manifest_chunks NOT INDEXED WHERE id IN (SELECT chunk_id FROM original.source_snapshot_chunks NOT INDEXED)`,
			"source_manifest_staging": `SELECT 0`,
		},
		Sources: map[string]string{
			"source_manifest_chunks": `FROM (SELECT DISTINCT chunk_id FROM original.source_snapshot_chunks NOT INDEXED) c
CROSS JOIN original.source_manifest_chunks e ON e.id = c.chunk_id`,
			"source_manifest_entries": `FROM (SELECT DISTINCT chunk_id FROM original.source_snapshot_chunks NOT INDEXED) c
CROSS JOIN original.source_manifest_entries e ON e.chunk_id = c.chunk_id`,
			"source_manifest_staging": `FROM original.source_manifest_staging e WHERE 0`,
		},
		// Reclamation checks surviving references independently. Reading every
		// discarded entry just to discover candidates would defeat the rebuild.
		After: []string{`INSERT OR IGNORE INTO source_blob_reclaim_queue SELECT sha256 FROM source_blob_objects`},
	}
}

func migrateManifestLayout(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, string(manifestLayoutSchema)); err != nil {
		return err
	}
	insert, err := prepareLayoutEntryInsert(ctx, tx, layoutInsertRows)
	if err != nil {
		return err
	}
	defer func() { _ = insert.Close() }()
	roots, err := legacySnapshotRoots(ctx, tx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		manifest, err := convertLegacyRoot(ctx, tx, insert, root.snapshotID, root.path)
		if err != nil {
			return fmt.Errorf("convert snapshot %s root %s: %w", root.snapshotID, root.path, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_snapshot_roots (snapshot_id, root_path, ordinal, manifest_id)
VALUES (?, ?, ?, ?)`, root.snapshotID, root.path, root.ordinal, manifest); err != nil {
			return err
		}
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

// convertLegacyRoot streams one root's retained entries in path order into
// content-defined chunks and records the manifest that lists them.
func convertLegacyRoot(ctx context.Context, tx *sql.Tx, insert *sql.Stmt, snapshotID, rootPath string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM legacy_source_snapshot_chunks c
CROSS JOIN legacy_source_manifest_entries e ON e.chunk_id = c.chunk_id
WHERE c.snapshot_id = ? AND e.root_path = ?
ORDER BY e.path`, snapshotID, rootPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var pending []layoutEntry
	var chunks []layoutChunk
	for rows.Next() {
		var entry layoutEntry
		if err := rows.Scan(&entry.path, &entry.sha256, &entry.gitOID, &entry.identity, &entry.size, &entry.mode, &entry.modifiedNS); err != nil {
			return "", err
		}
		pending = append(pending, entry)
		if layoutCutAfter(entry.path, len(pending)) {
			chunk, err := writeLayoutChunk(ctx, tx, insert, pending)
			if err != nil {
				return "", err
			}
			chunks, pending = append(chunks, chunk), pending[:0]
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(pending) > 0 {
		chunk, err := writeLayoutChunk(ctx, tx, insert, pending)
		if err != nil {
			return "", err
		}
		chunks = append(chunks, chunk)
	}
	return writeLayoutManifest(ctx, tx, chunks)
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

// validateManifestLayout requires every retained snapshot to list exactly
// the files it counted, through one manifest per root.
func validateManifestLayout(ctx context.Context, tx *sql.Tx) error {
	var mismatched int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_snapshots s
WHERE s.file_count != COALESCE((
    SELECT SUM(m.entry_count) FROM source_snapshot_roots r JOIN source_manifests m ON m.id = r.manifest_id
    WHERE r.snapshot_id = s.id), 0)`).Scan(&mismatched); err != nil {
		return err
	}
	if mismatched != 0 {
		return fmt.Errorf("%d retained snapshots lost entries in the manifest layout", mismatched)
	}
	return nil
}
