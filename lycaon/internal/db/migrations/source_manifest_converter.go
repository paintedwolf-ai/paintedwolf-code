package migrations

import (
	"container/heap"
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

const (
	legacyManifestMemoBytes = 16 << 20
	legacyWrittenMemoBytes  = 4 << 20
	// legacyPageEntries bounds the entries one legacy chunk holds in memory.
	legacyPageEntries = 256
)

// legacyManifestConverter streams each root's entries in path order, so a
// conversion holds a page per legacy chunk and one target chunk, never a root.
type legacyManifestConverter struct {
	tx            *sql.Tx
	insert        *sql.Stmt
	holdsRoot     *sql.Stmt
	readPage      *sql.Stmt
	manifests     *migrationMemo[string]
	writtenChunks *migrationMemo[struct{}]
}

func newLegacyManifestConverter(ctx context.Context, tx *sql.Tx, insert *sql.Stmt) (*legacyManifestConverter, error) {
	// Both reads search the legacy table's unique (chunk_id, root_path, path) index.
	holdsRoot, err := tx.PrepareContext(ctx, `SELECT EXISTS (SELECT 1 FROM legacy_source_manifest_entries WHERE chunk_id = ? AND root_path = ?)`)
	if err != nil {
		return nil, err
	}
	readPage, err := tx.PrepareContext(ctx, `SELECT path, sha256, git_oid, identity, size, mode, modified_ns
FROM legacy_source_manifest_entries WHERE chunk_id = ? AND root_path = ? AND path > ? ORDER BY path LIMIT `+strconv.Itoa(legacyPageEntries))
	if err != nil {
		_ = holdsRoot.Close()
		return nil, err
	}
	return &legacyManifestConverter{
		tx: tx, insert: insert, holdsRoot: holdsRoot, readPage: readPage,
		manifests:     newMigrationMemo[string](legacyManifestMemoBytes),
		writtenChunks: newMigrationMemo[struct{}](legacyWrittenMemoBytes),
	}, nil
}

func (c *legacyManifestConverter) close() error {
	return errors.Join(c.holdsRoot.Close(), c.readPage.Close())
}

func legacyRootManifestKey(rootPath string, chunkIDs []string) string {
	return strconv.Itoa(len(rootPath)) + ":" + rootPath + "\x00" + strings.Join(chunkIDs, "\x00")
}

func (c *legacyManifestConverter) convertRoot(ctx context.Context, rootPath string, chunkIDs []string) (string, error) {
	key := legacyRootManifestKey(rootPath, chunkIDs)
	if manifest, ok := c.manifests.load(key); ok {
		return manifest, nil
	}
	var active []string
	for _, chunkID := range chunkIDs {
		var holds bool
		if err := c.holdsRoot.QueryRowContext(ctx, chunkID, rootPath).Scan(&holds); err != nil {
			return "", err
		}
		if holds {
			active = append(active, chunkID)
		}
	}
	activeKey := legacyRootManifestKey(rootPath, active)
	if manifest, ok := c.manifests.load(activeKey); ok {
		c.memoManifest(key, manifest)
		return manifest, nil
	}
	merge := &legacyRootMerge{read: c.readPage, root: rootPath}
	for order, chunkID := range active {
		merge.cursors = append(merge.cursors, &legacyCursor{chunk: chunkID, order: order})
	}
	manifest, err := c.writeRootManifest(ctx, merge)
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

func (c *legacyManifestConverter) writeRootManifest(ctx context.Context, merge *legacyRootMerge) (string, error) {
	var pending []layoutEntry
	var chunks []layoutChunk
	for {
		entry, ok, err := merge.next(ctx)
		if err != nil {
			return "", err
		}
		if !ok {
			break
		}
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

// legacyCursor pages one legacy chunk's entries of a root in path order.
type legacyCursor struct {
	chunk     string
	order     int
	page      []layoutEntry
	next      int
	exhausted bool
}

// legacyRootMerge yields a root's entries from all its chunks in path order;
// equal paths keep chunk order, as a stable sort of the chunks would.
type legacyRootMerge struct {
	read    *sql.Stmt
	root    string
	cursors []*legacyCursor
	ready   legacyHeap
	started bool
}

func (m *legacyRootMerge) next(ctx context.Context) (layoutEntry, bool, error) {
	if !m.started {
		m.started = true
		for _, cursor := range m.cursors {
			if err := m.advance(ctx, cursor); err != nil {
				return layoutEntry{}, false, err
			}
		}
	}
	if m.ready.Len() == 0 {
		return layoutEntry{}, false, nil
	}
	cursor := heap.Pop(&m.ready).(*legacyCursor)
	entry := cursor.page[cursor.next]
	cursor.next++
	if err := m.advance(ctx, cursor); err != nil {
		return layoutEntry{}, false, err
	}
	return entry, true, nil
}

// advance returns the cursor to the heap if it holds an entry, reading its
// next page after the last path it yielded.
func (m *legacyRootMerge) advance(ctx context.Context, cursor *legacyCursor) error {
	if cursor.next < len(cursor.page) {
		heap.Push(&m.ready, cursor)
		return nil
	}
	if cursor.exhausted {
		return nil
	}
	after := ""
	if len(cursor.page) > 0 {
		after = cursor.page[len(cursor.page)-1].path
	}
	rows, err := m.read.QueryContext(ctx, cursor.chunk, m.root, after)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	cursor.page, cursor.next = cursor.page[:0], 0
	for rows.Next() {
		var entry layoutEntry
		if err := rows.Scan(&entry.path, &entry.sha256, &entry.gitOID, &entry.identity, &entry.size, &entry.mode, &entry.modifiedNS); err != nil {
			return err
		}
		cursor.page = append(cursor.page, entry)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cursor.exhausted = len(cursor.page) < legacyPageEntries
	if len(cursor.page) > 0 {
		heap.Push(&m.ready, cursor)
	}
	return nil
}

type legacyHeap []*legacyCursor

func (h legacyHeap) Len() int { return len(h) }
func (h legacyHeap) Less(i, j int) bool {
	a, b := h[i].page[h[i].next].path, h[j].page[h[j].next].path
	if a != b {
		return a < b
	}
	return h[i].order < h[j].order
}
func (h legacyHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *legacyHeap) Push(x any)   { *h = append(*h, x.(*legacyCursor)) }
func (h *legacyHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}
