package sourcesnapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// Manifests share unchanged chunks across 256 content-addressed buckets.

type bucketChunk struct {
	bucket     byte
	id         string
	entryCount int64
	totalBytes int64
}

func (s *Store) publish(ctx context.Context, req Request, candidates []*rootCandidate, boundaries []Boundary, quality CaptureQuality, admissionMode AdmissionMode) (Snapshot, error) {
	boundaries = append([]Boundary(nil), boundaries...)
	sort.Slice(boundaries, func(i, j int) bool {
		if boundaries[i].RootPath != boundaries[j].RootPath {
			return boundaries[i].RootPath < boundaries[j].RootPath
		}
		return boundaries[i].Path < boundaries[j].Path
	})
	out := Snapshot{
		RootsKey: rootsKey(req.Roots), Quality: quality.Normalized(),
		AdmissionMode: AdmissionMode(strings.TrimSpace(string(admissionMode))),
		CreatedAt:     time.Now().UTC(), Roots: append([]Root(nil), req.Roots...),
		Boundaries: boundaries,
	}
	for _, c := range candidates {
		if err := c.flushStaging(ctx, s); err != nil {
			return Snapshot{}, err
		}
	}
	// Chunks commit independently; failed publications leave unreferenced chunks for collection.
	chunks, err := s.buildChunks(ctx, req, candidates)
	if err != nil {
		return Snapshot{}, err
	}
	for _, chunk := range chunks {
		out.FileCount += int(chunk.entryCount)
		out.TotalBytes += chunk.totalBytes
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	out.MerkleSHA256 = manifestHash(out.Roots, chunks)
	out.ID = out.MerkleSHA256
	if err := q.UpsertSourceSnapshot(ctx, db.UpsertSourceSnapshotParams{
		ID: out.ID, RootsKey: out.RootsKey, MerkleSha256: out.MerkleSHA256,
		FileCount: int64(out.FileCount), TotalBytes: out.TotalBytes,
		CaptureQuality: string(out.Quality), CreatedTs: db.FormatTime(out.CreatedAt),
	}); err != nil {
		return Snapshot{}, err
	}
	for ordinal, root := range out.Roots {
		if err := q.InsertSourceSnapshotRoot(ctx, db.InsertSourceSnapshotRootParams{
			SnapshotID: out.ID, RootPath: root.Path, Ordinal: int64(ordinal),
		}); err != nil {
			return Snapshot{}, err
		}
	}
	for _, chunk := range chunks {
		if err := q.InsertSourceSnapshotChunk(ctx, db.InsertSourceSnapshotChunkParams{
			SnapshotID: out.ID, Bucket: int64(chunk.bucket), ChunkID: chunk.id,
		}); err != nil {
			return Snapshot{}, err
		}
	}
	for _, b := range out.Boundaries {
		if err := q.InsertSourceSnapshotBoundary(ctx, db.InsertSourceSnapshotBoundaryParams{
			SnapshotID: out.ID, RootPath: b.RootPath, Path: b.Path,
			Reason: b.Reason, Detail: b.Detail, Entries: int64(b.Entries),
		}); err != nil {
			return Snapshot{}, err
		}
	}
	if err := q.UpsertSourceSnapshotHead(ctx, db.UpsertSourceSnapshotHeadParams{
		RootsKey: out.RootsKey, SnapshotID: out.ID,
		PublishedTs: db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return Snapshot{}, err
	}
	return out, tx.Commit()
}

// buildChunks reuses untouched buckets when the base has identical roots.
func (s *Store) buildChunks(ctx context.Context, req Request, candidates []*rootCandidate) ([]bucketChunk, error) {
	base, baseChunks, err := s.sharedBase(ctx, req, candidates)
	if err != nil {
		return nil, err
	}
	chunks := make([]bucketChunk, 0, 256)
	for bucket := range 256 {
		b := byte(bucket)
		if base != "" && !anyTouched(candidates, b) {
			id, ok := baseChunks[b]
			if !ok {
				continue
			}
			row, err := s.queries.GetSourceManifestChunk(ctx, id)
			if err != nil {
				return nil, err
			}
			chunks = append(chunks, bucketChunk{bucket: b, id: id, entryCount: row.EntryCount, totalBytes: row.TotalBytes})
			continue
		}
		var entries []Entry
		for _, c := range candidates {
			part, err := c.bucketEntries(ctx, s.queries, b)
			if err != nil {
				return nil, err
			}
			entries = append(entries, part...)
		}
		if len(entries) == 0 {
			continue
		}
		sortEntries(entries)
		chunk := bucketChunk{bucket: b, id: manifestChunkHash(b, entries), entryCount: int64(len(entries))}
		for _, entry := range entries {
			chunk.totalBytes += entry.Size
		}
		if err := s.insertChunk(ctx, chunk, entries); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

// sharedBase requires every candidate to share a base with identical roots.
func (s *Store) sharedBase(ctx context.Context, req Request, candidates []*rootCandidate) (string, map[byte]string, error) {
	q := s.queries
	if len(candidates) == 0 {
		return "", nil, nil
	}
	base := candidates[0].base
	for _, c := range candidates {
		if !c.isDelta() || c.base != base {
			return "", nil, nil
		}
	}
	roots, err := q.ListSourceSnapshotRoots(ctx, base)
	if err != nil {
		return "", nil, err
	}
	if len(roots) != len(req.Roots) {
		return "", nil, nil
	}
	for i, root := range roots {
		if root.RootPath != req.Roots[i].Path {
			return "", nil, nil
		}
	}
	return base, candidates[0].baseChunks, nil
}

func anyTouched(candidates []*rootCandidate, bucket byte) bool {
	for _, c := range candidates {
		if c.touched(bucket) {
			return true
		}
	}
	return false
}

// insertChunk stores one chunk's content in its own transaction, unless
// the store already holds it.
func (s *Store) insertChunk(ctx context.Context, chunk bucketChunk, entries []Entry) error {
	exists, err := s.queries.SourceManifestChunkExists(ctx, chunk.id)
	if err != nil || exists != 0 {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := q.InsertSourceManifestChunk(ctx, db.InsertSourceManifestChunkParams{
		ID: chunk.id, EntryCount: chunk.entryCount, TotalBytes: chunk.totalBytes,
	}); err != nil {
		return err
	}
	for ordinal, entry := range entries {
		if err := q.InsertSourceManifestEntry(ctx, db.InsertSourceManifestEntryParams{
			ChunkID: chunk.id, Ordinal: int64(ordinal), RootPath: entry.RootPath, Path: entry.Path,
			Sha256: entry.SHA256, GitOid: entry.GitOID, Identity: string(entry.Identity),
			Size: entry.Size, Mode: int64(entry.Mode), ModifiedNs: entry.ModifiedNS,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func manifestChunkHash(bucket byte, entries []Entry) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("painted-wolf-source-manifest-chunk-v2\x00"))
	_, _ = hash.Write([]byte{bucket})
	for _, entry := range entries {
		_, _ = hash.Write([]byte(entry.RootPath + "\x00" + entry.Path + "\x00" + entry.contentKey() + "\x00"))
		_, _ = hash.Write([]byte(strconv.FormatInt(entry.Size, 10) + "\x00" + strconv.FormatUint(uint64(entry.Mode), 10) + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func manifestHash(roots []Root, chunks []bucketChunk) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("painted-wolf-source-snapshot-v2\x00"))
	for _, root := range roots {
		_, _ = hash.Write([]byte(root.Path + "\x00"))
	}
	for _, chunk := range chunks {
		_, _ = hash.Write([]byte{chunk.bucket})
		_, _ = hash.Write([]byte(chunk.id + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// ErrSnapshotNotFound means the identity is not resident in this store.
var ErrSnapshotNotFound = errors.New("source snapshot not found")

func snapshotError(id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrSnapshotNotFound, id)
	}
	return err
}

// Get loads snapshot metadata; entries are read separately.
func (s *Store) Get(ctx context.Context, id string) (Snapshot, error) {
	row, err := s.queries.GetSourceSnapshot(ctx, id)
	if err != nil {
		return Snapshot{}, snapshotError(id, err)
	}
	roots, err := s.queries.ListSourceSnapshotRoots(ctx, row.ID)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{
		ID: row.ID, RootsKey: row.RootsKey, MerkleSHA256: row.MerkleSha256,
		FileCount: int(row.FileCount), TotalBytes: row.TotalBytes,
		Quality: CaptureQuality(row.CaptureQuality).Normalized(),
		Roots:   make([]Root, 0, len(roots)),
	}
	out.CreatedAt, _ = db.ParseTime(row.CreatedTs)
	for _, root := range roots {
		out.Roots = append(out.Roots, Root{Path: root.RootPath})
	}
	boundaries, err := s.queries.ListSourceSnapshotBoundaries(ctx, row.ID)
	if err != nil {
		return Snapshot{}, err
	}
	for _, b := range boundaries {
		out.Boundaries = append(out.Boundaries, Boundary{
			RootPath: b.RootPath, Path: b.Path, Reason: b.Reason, Detail: b.Detail, Entries: int(b.Entries),
		})
		if (Boundary{Reason: b.Reason}).Budgeted() {
			out.AdmissionMode = AdmissionScopeBounded
		}
	}
	if out.AdmissionMode == "" {
		out.AdmissionMode = AdmissionScope
	}
	return out, nil
}

// Lookup returns one admitted file of a snapshot, or false when the
// generation does not hold the path.
func (s *Store) Lookup(ctx context.Context, id, rootPath, rel string) (Entry, bool, error) {
	row, err := s.queries.GetSourceSnapshotEntry(ctx, db.GetSourceSnapshotEntryParams{
		SnapshotID: id, RootPath: rootPath, Path: rel,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	entry, err := entryFrom(row.RootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
	return entry, err == nil, err
}

// EntriesUnder lists the admitted files at or below one root-relative
// path; "." names the whole root. A file path lists itself.
func (s *Store) EntriesUnder(ctx context.Context, id, rootPath, dir string) ([]Entry, error) {
	dir = cleanRelDir(dir)
	if dir == "." {
		rows, err := s.queries.ListSourceSnapshotRootEntries(ctx, db.ListSourceSnapshotRootEntriesParams{
			SnapshotID: id, RootPath: rootPath,
		})
		if err != nil {
			return nil, err
		}
		return entriesFromRootRows(rows)
	}
	var out []Entry
	if exact, ok, err := s.Lookup(ctx, id, rootPath, dir); err != nil {
		return nil, err
	} else if ok {
		out = append(out, exact)
	}
	low, high := pathBounds(dir)
	rows, err := s.queries.ListSourceSnapshotEntriesBelow(ctx, db.ListSourceSnapshotEntriesBelowParams{
		SnapshotID: id, RootPath: rootPath, Low: low, High: high,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		entry, err := entryFrom(row.RootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

func entriesFromRootRows(rows []db.ListSourceSnapshotRootEntriesRow) ([]Entry, error) {
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entry, err := entryFrom(row.RootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// HashedEntries lists files whose bytes need explicit retention, bounded by size and count.
func (s *Store) HashedEntries(ctx context.Context, id string, maxSize int64, limit int) ([]Entry, error) {
	rows, err := s.queries.ListSourceSnapshotHashedEntries(ctx, db.ListSourceSnapshotHashedEntriesParams{
		SnapshotID: id, MaxSize: maxSize, RowLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entry, err := entryFrom(row.RootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// HasPath reports whether a snapshot holds rel as a file or as a directory
// with an admitted file below it.
func (s *Store) HasPath(ctx context.Context, id, rootPath, rel string) (bool, error) {
	rel = cleanRelDir(rel)
	if rel == "." {
		held, err := s.queries.SourceSnapshotHoldsRoot(ctx, db.SourceSnapshotHoldsRootParams{SnapshotID: id, RootPath: rootPath})
		return held != 0, err
	}
	if _, ok, err := s.Lookup(ctx, id, rootPath, rel); err != nil || ok {
		return ok, err
	}
	low, high := pathBounds(rel)
	held, err := s.queries.SourceSnapshotHoldsBelow(ctx, db.SourceSnapshotHoldsBelowParams{
		SnapshotID: id, RootPath: rootPath, Low: low, High: high,
	})
	return held != 0, err
}

// ForEachEntry streams admitted files in root and path order, one bucket at a time.
func (s *Store) ForEachEntry(ctx context.Context, id string, fn func(Entry) error) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.root_path, e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM source_snapshot_chunks c
JOIN source_manifest_entries e ON e.chunk_id = c.chunk_id
WHERE c.snapshot_id = ?
ORDER BY e.root_path, e.path`, id)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rootPath, path, sha, oid, identity string
		var size, mode, modifiedNS int64
		if err := rows.Scan(&rootPath, &path, &sha, &oid, &identity, &size, &mode, &modifiedNS); err != nil {
			return err
		}
		entry, err := entryFrom(rootPath, path, sha, oid, identity, size, mode, modifiedNS)
		if err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Diff reports every path that differs between two generations, reading
// only the buckets whose chunks differ. An empty previous id makes every
// entry of current an addition.
func (s *Store) Diff(ctx context.Context, previousID, currentID string, fn func(Change) error) error {
	previous, err := s.chunkMap(ctx, previousID)
	if err != nil {
		return err
	}
	current, err := s.chunkMap(ctx, currentID)
	if err != nil {
		return err
	}
	for bucket := range 256 {
		b := byte(bucket)
		before, after := previous[b], current[b]
		if before == after {
			continue
		}
		beforeEntries, err := s.chunkEntries(ctx, before)
		if err != nil {
			return err
		}
		afterEntries, err := s.chunkEntries(ctx, after)
		if err != nil {
			return err
		}
		if err := diffBucket(beforeEntries, afterEntries, fn); err != nil {
			return err
		}
	}
	return nil
}

func diffBucket(before, after []Entry, fn func(Change) error) error {
	held := make(map[string]int, len(before))
	for i, entry := range before {
		held[entry.RootPath+"\x00"+entry.Path] = i
	}
	seen := make(map[int]struct{}, len(after))
	for i := range after {
		entry := after[i]
		j, ok := held[entry.RootPath+"\x00"+entry.Path]
		if !ok {
			if err := fn(Change{After: &entry}); err != nil {
				return err
			}
			continue
		}
		seen[j] = struct{}{}
		if !before[j].SameContent(entry) || before[j].Mode != entry.Mode {
			if err := fn(Change{Before: &before[j], After: &entry}); err != nil {
				return err
			}
		}
	}
	for i := range before {
		if _, ok := seen[i]; ok {
			continue
		}
		if err := fn(Change{Before: &before[i]}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) chunkMap(ctx context.Context, id string) (map[byte]string, error) {
	out := make(map[byte]string)
	if id == "" {
		return out, nil
	}
	rows, err := s.queries.ListSourceSnapshotChunks(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Bucket < 0 || row.Bucket > 255 {
			return nil, fmt.Errorf("snapshot %s names bucket %d", id, row.Bucket)
		}
		out[byte(row.Bucket)] = row.ChunkID
	}
	return out, nil
}

func (s *Store) chunkEntries(ctx context.Context, chunkID string) ([]Entry, error) {
	if chunkID == "" {
		return nil, nil
	}
	rows, err := s.queries.ListSourceManifestChunkEntries(ctx, chunkID)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entry, err := entryFrom(row.RootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}
