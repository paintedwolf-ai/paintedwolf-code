package sourcesnapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
)

// ErrSnapshotNotFound means the identity is not resident in this store.
var ErrSnapshotNotFound = errors.New("source snapshot not found")

func snapshotError(id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrSnapshotNotFound, id)
	}
	return err
}

// snapshotView is a snapshot whose manifests stay readable until release:
// every read of a generation goes through one, a chunk or a page per query,
// so no read holds a transaction open across the generation.
type snapshotView struct {
	Snapshot
	manifests map[string]string
	pin       *pin
}

func (v *snapshotView) release() { v.pin.release() }

// open pins a snapshot's manifests and confirms they survived to the pin;
// a sweep that ran first has removed them, and the snapshot is gone too.
func (s *Store) open(ctx context.Context, id string) (*snapshotView, error) {
	snapshot, manifests, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	view := &snapshotView{Snapshot: snapshot, manifests: make(map[string]string, len(manifests))}
	for i, root := range snapshot.Roots {
		view.manifests[root.Path] = manifests[i]
	}
	view.pin = s.pins.holdManifests(manifests...)
	for _, manifest := range manifests {
		if _, err := s.queries.GetSourceManifest(ctx, manifest); err != nil {
			view.release()
			return nil, snapshotError(id, err)
		}
	}
	return view, nil
}

func (s *Store) load(ctx context.Context, id string) (Snapshot, []string, error) {
	row, err := s.queries.GetSourceSnapshot(ctx, id)
	if err != nil {
		return Snapshot{}, nil, snapshotError(id, err)
	}
	roots, err := s.queries.ListSourceSnapshotRoots(ctx, row.ID)
	if err != nil {
		return Snapshot{}, nil, err
	}
	out := Snapshot{
		ID: row.ID, RootsKey: row.RootsKey, MerkleSHA256: row.MerkleSha256,
		FileCount: int(row.FileCount), TotalBytes: row.TotalBytes,
		Quality: CaptureQuality(row.CaptureQuality).Normalized(),
		Roots:   make([]Root, 0, len(roots)),
	}
	out.CreatedAt, _ = db.ParseTime(row.CreatedTs)
	manifests := make([]string, 0, len(roots))
	for _, root := range roots {
		out.Roots = append(out.Roots, Root{Path: root.RootPath})
		manifests = append(manifests, root.ManifestID)
	}
	boundaries, err := s.queries.ListSourceSnapshotBoundaries(ctx, row.ID)
	if err != nil {
		return Snapshot{}, nil, err
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
	return out, manifests, nil
}

// Get loads snapshot metadata; entries are read separately.
func (s *Store) Get(ctx context.Context, id string) (Snapshot, error) {
	snapshot, _, err := s.load(ctx, id)
	return snapshot, err
}

// Lookup returns one admitted file of a snapshot, or false when the
// generation does not hold the path.
func (s *Store) Lookup(ctx context.Context, id, rootPath, rel string) (Entry, bool, error) {
	view, err := s.open(ctx, id)
	if err != nil {
		return Entry{}, false, err
	}
	defer view.release()
	return s.lookup(ctx, view, rootPath, rel)
}

func (s *Store) lookup(ctx context.Context, view *snapshotView, rootPath, rel string) (Entry, bool, error) {
	manifest, ok := view.manifests[rootPath]
	if !ok {
		return Entry{}, false, nil
	}
	return s.manifestLookup(ctx, manifest, rootPath, rel)
}

func (s *Store) manifestLookup(ctx context.Context, manifest, rootPath, rel string) (Entry, bool, error) {
	row, err := s.queries.GetSourceManifestEntry(ctx, db.GetSourceManifestEntryParams{ManifestID: manifest, Path: rel})
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	entry, err := entryFrom(rootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
	return entry, err == nil, err
}

// EntriesUnder lists the admitted files at or below one root-relative
// path; "." names the whole root. A file path lists itself.
func (s *Store) EntriesUnder(ctx context.Context, id, rootPath, dir string) ([]Entry, error) {
	view, err := s.open(ctx, id)
	if err != nil {
		return nil, err
	}
	defer view.release()
	manifest, ok := view.manifests[rootPath]
	if !ok {
		return nil, nil
	}
	var out []Entry
	err = s.manifestEntriesUnder(ctx, manifest, rootPath, dir, func(entry Entry) error {
		out = append(out, entry)
		return nil
	})
	return out, err
}

func (s *Store) manifestEntriesUnder(ctx context.Context, manifest, rootPath, dir string, fn func(Entry) error) error {
	dir = cleanRelDir(dir)
	if dir == "." {
		return s.forEachManifestEntry(ctx, manifest, rootPath, fn)
	}
	if exact, ok, err := s.manifestLookup(ctx, manifest, rootPath, dir); err != nil {
		return err
	} else if ok {
		if err := fn(exact); err != nil {
			return err
		}
	}
	low, high := pathBounds(dir)
	chunks, err := s.queries.ListSourceManifestLayoutBetween(ctx, db.ListSourceManifestLayoutBetweenParams{
		ManifestID: manifest, Low: low, High: high,
	})
	if err != nil {
		return err
	}
	for _, chunk := range chunks {
		rows, err := s.queries.ListSourceManifestChunkEntriesBetween(ctx, db.ListSourceManifestChunkEntriesBetweenParams{
			ChunkID: chunk.ChunkID, Low: low, High: high,
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			entry, err := entryFrom(rootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
			if err != nil {
				return err
			}
			if err := fn(entry); err != nil {
				return err
			}
		}
	}
	return nil
}

// HashedEntries lists files whose bytes need explicit retention, bounded by size and count.
func (s *Store) HashedEntries(ctx context.Context, id string, maxSize int64, limit int) ([]Entry, error) {
	view, err := s.open(ctx, id)
	if err != nil {
		return nil, err
	}
	defer view.release()
	var out []Entry
	for _, root := range view.Roots {
		if len(out) >= limit {
			break
		}
		rows, err := s.queries.ListSourceManifestHashedEntries(ctx, db.ListSourceManifestHashedEntriesParams{
			ManifestID: view.manifests[root.Path], MaxSize: maxSize, RowLimit: int64(limit - len(out)),
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			entry, err := entryFrom(root.Path, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
			if err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
	}
	return out, nil
}

// HasPath reports whether a snapshot holds rel as a file or as a directory
// with an admitted file below it.
func (s *Store) HasPath(ctx context.Context, id, rootPath, rel string) (bool, error) {
	view, err := s.open(ctx, id)
	if err != nil {
		return false, err
	}
	defer view.release()
	manifest, ok := view.manifests[rootPath]
	if !ok {
		return false, nil
	}
	rel = cleanRelDir(rel)
	if rel == "." {
		row, err := s.queries.GetSourceManifest(ctx, manifest)
		return err == nil && row.EntryCount > 0, err
	}
	if _, ok, err := s.manifestLookup(ctx, manifest, rootPath, rel); err != nil || ok {
		return ok, err
	}
	low, high := pathBounds(rel)
	held, err := s.queries.SourceManifestHoldsBetween(ctx, db.SourceManifestHoldsBetweenParams{
		ManifestID: manifest, Low: low, High: high,
	})
	return held != 0, err
}

// ForEachEntry streams admitted files in root and path order, one chunk at a time.
func (s *Store) ForEachEntry(ctx context.Context, id string, fn func(Entry) error) error {
	view, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	defer view.release()
	return s.forEachViewEntry(ctx, view, fn)
}

func (s *Store) forEachViewEntry(ctx context.Context, view *snapshotView, fn func(Entry) error) error {
	for _, root := range view.Roots {
		if err := s.forEachManifestEntry(ctx, view.manifests[root.Path], root.Path, fn); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) forEachManifestEntry(ctx context.Context, manifest, rootPath string, fn func(Entry) error) error {
	layout, err := s.manifestLayout(ctx, manifest)
	if err != nil {
		return err
	}
	for _, chunk := range layout {
		entries, err := s.chunkEntries(ctx, chunk.id, rootPath)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := fn(entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) manifestLayout(ctx context.Context, manifest string) ([]manifestChunk, error) {
	rows, err := s.queries.ListSourceManifestLayout(ctx, manifest)
	if err != nil {
		return nil, err
	}
	out := make([]manifestChunk, 0, len(rows))
	for _, row := range rows {
		out = append(out, manifestChunk{
			firstPath: row.FirstPath, id: row.ChunkID,
			entryCount: row.EntryCount, totalBytes: row.TotalBytes, hashed: row.HashedCount,
		})
	}
	return out, nil
}

func (s *Store) chunkEntries(ctx context.Context, chunkID, rootPath string) ([]Entry, error) {
	rows, err := s.queries.ListSourceManifestChunkEntries(ctx, chunkID)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entry, err := entryFrom(rootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}
