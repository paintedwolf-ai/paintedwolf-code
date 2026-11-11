package migrations

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"strings"
)

const (
	legacyDecodedMemoBytes  = 64 << 20
	legacyManifestMemoBytes = 16 << 20
	legacyWrittenMemoBytes  = 4 << 20
)

type legacyManifestConverter struct {
	tx            *sql.Tx
	insert        *sql.Stmt
	readChunk     *sql.Stmt
	chunks        *migrationMemo[map[string][]layoutEntry]
	manifests     *migrationMemo[string]
	writtenChunks *migrationMemo[struct{}]
}

func newLegacyManifestConverter(tx *sql.Tx, insert, readChunk *sql.Stmt) *legacyManifestConverter {
	return &legacyManifestConverter{
		tx: tx, insert: insert, readChunk: readChunk,
		chunks:        newMigrationMemo[map[string][]layoutEntry](legacyDecodedMemoBytes),
		manifests:     newMigrationMemo[string](legacyManifestMemoBytes),
		writtenChunks: newMigrationMemo[struct{}](legacyWrittenMemoBytes),
	}
}

func legacyRootManifestKey(rootPath string, chunkIDs []string) string {
	return strconv.Itoa(len(rootPath)) + ":" + rootPath + "\x00" + strings.Join(chunkIDs, "\x00")
}

// convertRoot retains only this root's working entries beyond the bounded memos.
func (c *legacyManifestConverter) convertRoot(ctx context.Context, rootPath string, chunkIDs []string) (string, error) {
	key := legacyRootManifestKey(rootPath, chunkIDs)
	if manifest, ok := c.manifests.load(key); ok {
		return manifest, nil
	}
	var active []string
	var selected [][]layoutEntry
	var total int
	for _, chunkID := range chunkIDs {
		byRoot, err := c.decodedChunk(ctx, chunkID)
		if err != nil {
			return "", err
		}
		if entries := byRoot[rootPath]; len(entries) > 0 {
			active = append(active, chunkID)
			selected = append(selected, entries)
			total += len(entries)
		}
	}
	activeKey := legacyRootManifestKey(rootPath, active)
	if manifest, ok := c.manifests.load(activeKey); ok {
		c.memoManifest(key, manifest)
		return manifest, nil
	}
	entries := make([]layoutEntry, 0, total)
	for _, chunk := range selected {
		entries = append(entries, chunk...)
	}
	slices.SortStableFunc(entries, func(a, b layoutEntry) int { return strings.Compare(a.path, b.path) })
	manifest, err := c.writeRootManifest(ctx, entries)
	if err != nil {
		return "", err
	}
	c.memoManifest(key, manifest)
	c.memoManifest(activeKey, manifest)
	return manifest, nil
}

func (c *legacyManifestConverter) memoManifest(key, manifest string) {
	c.manifests.store(key, manifest, int64(len(key)+len(manifest))+64)
}

func (c *legacyManifestConverter) decodedChunk(ctx context.Context, chunkID string) (map[string][]layoutEntry, error) {
	if chunk, ok := c.chunks.load(chunkID); ok {
		return chunk, nil
	}
	rows, err := c.readChunk.QueryContext(ctx, chunkID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byRoot := map[string][]layoutEntry{}
	weight := int64(len(chunkID)) + 64
	for rows.Next() {
		var rootPath string
		var entry layoutEntry
		if err := rows.Scan(&rootPath, &entry.path, &entry.sha256, &entry.gitOID, &entry.identity, &entry.size, &entry.mode, &entry.modifiedNS); err != nil {
			return nil, err
		}
		byRoot[rootPath] = append(byRoot[rootPath], entry)
		if weight <= legacyDecodedMemoBytes {
			weight += int64(len(rootPath)+len(entry.path)+len(entry.sha256)+len(entry.gitOID)+len(entry.identity)) + 152
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	c.chunks.store(chunkID, byRoot, weight)
	return byRoot, nil
}

func (c *legacyManifestConverter) writeRootManifest(ctx context.Context, entries []layoutEntry) (string, error) {
	var pending []layoutEntry
	var chunks []layoutChunk
	for _, entry := range entries {
		pending = append(pending, entry)
		if layoutCutAfter(entry.path, len(pending)) {
			chunk, err := c.writeChunk(ctx, pending)
			if err != nil {
				return "", err
			}
			chunks, pending = append(chunks, chunk), pending[:0]
		}
	}
	if len(pending) > 0 {
		chunk, err := c.writeChunk(ctx, pending)
		if err != nil {
			return "", err
		}
		chunks = append(chunks, chunk)
	}
	return writeLayoutManifest(ctx, c.tx, chunks)
}

func (c *legacyManifestConverter) writeChunk(ctx context.Context, entries []layoutEntry) (layoutChunk, error) {
	chunk := buildLayoutChunk(entries)
	if _, ok := c.writtenChunks.load(chunk.id); ok {
		return chunk, nil
	}
	if _, err := insertLayoutChunk(ctx, c.tx, c.insert, chunk, entries); err != nil {
		return layoutChunk{}, err
	}
	c.writtenChunks.store(chunk.id, struct{}{}, int64(len(chunk.id))+64)
	return chunk, nil
}
