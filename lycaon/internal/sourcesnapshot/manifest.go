package sourcesnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"sort"
	"strconv"

	"github.com/lycaon/lycaon/internal/db"
)

// A manifest is one root's entries in path order, cut into chunks where a
// path's own hash says so. Cuts depend only on the paths around them, so an
// edit rewrites the chunk that holds it and a rename or a new file moves at
// most the cuts beside it.
const (
	chunkMinEntries = 128
	chunkMaxEntries = 4096
	// chunkCutMask gives a cut after one path in 512 on average.
	chunkCutMask = 511
	// stagingPage bounds one read of a survey's staged entries.
	stagingPage = 2048
)

// manifestChunk is one run of a manifest as its layout names it.
type manifestChunk struct {
	firstPath  string
	id         string
	entryCount int64
	totalBytes int64
	hashed     int64
}

// rootManifest is a built manifest whose chunks and row are written.
type rootManifest struct {
	id         string
	entryCount int64
	totalBytes int64
}

// cutAfter reports whether a chunk of n entries ends at path.
func cutAfter(path string, n int) bool {
	if n >= chunkMaxEntries {
		return true
	}
	if n < chunkMinEntries {
		return false
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	return h.Sum64()&chunkCutMask == 0
}

// manifestBuilder streams entries in path order into chunks and records the
// layout. Every chunk it writes or reuses stays pinned until the caller
// releases the pin, so a sweep cannot remove it before a manifest names it.
type manifestBuilder struct {
	store   *Store
	pin     *pin
	pending []Entry
	chunks  []manifestChunk
}

func (s *Store) newManifestBuilder(held *pin) *manifestBuilder {
	return &manifestBuilder{store: s, pin: held}
}

func (b *manifestBuilder) add(ctx context.Context, entry Entry) error {
	b.pending = append(b.pending, entry)
	if cutAfter(entry.Path, len(b.pending)) {
		return b.cut(ctx)
	}
	return nil
}

// aligned reports whether the stream sits at a cut, where an unchanged base
// chunk can follow as it is.
func (b *manifestBuilder) aligned() bool { return len(b.pending) == 0 }

func (b *manifestBuilder) reuse(chunk manifestChunk) { b.chunks = append(b.chunks, chunk) }

func (b *manifestBuilder) cut(ctx context.Context) error {
	if len(b.pending) == 0 {
		return nil
	}
	chunk := manifestChunk{firstPath: b.pending[0].Path, id: chunkHash(b.pending), entryCount: int64(len(b.pending))}
	for _, entry := range b.pending {
		chunk.totalBytes += entry.Size
		if entry.Identity == IdentityHashed {
			chunk.hashed++
		}
	}
	b.pin.addChunk(chunk.id)
	if err := b.store.insertChunk(ctx, chunk, b.pending); err != nil {
		return err
	}
	b.chunks = append(b.chunks, chunk)
	b.pending = b.pending[:0]
	return nil
}

// finish writes the last chunk and the manifest row with its layout.
func (b *manifestBuilder) finish(ctx context.Context) (rootManifest, error) {
	if err := b.cut(ctx); err != nil {
		return rootManifest{}, err
	}
	out := rootManifest{id: manifestHash(b.chunks)}
	for _, chunk := range b.chunks {
		out.entryCount += chunk.entryCount
		out.totalBytes += chunk.totalBytes
	}
	b.pin.addManifest(out.id)
	return out, b.store.insertManifest(ctx, out, b.chunks)
}

// insertChunk writes one chunk and its entries in one transaction unless the
// store already holds it.
func (s *Store) insertChunk(ctx context.Context, chunk manifestChunk, entries []Entry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	inserted, err := q.InsertSourceManifestChunk(ctx, db.InsertSourceManifestChunkParams{
		ID: chunk.id, EntryCount: chunk.entryCount, TotalBytes: chunk.totalBytes, HashedCount: chunk.hashed,
	})
	if err != nil || inserted == 0 {
		return err
	}
	for _, entry := range entries {
		if err := q.InsertSourceManifestEntry(ctx, db.InsertSourceManifestEntryParams{
			ChunkID: chunk.id, Path: entry.Path,
			Sha256: entry.SHA256, GitOid: entry.GitOID, Identity: string(entry.Identity),
			Size: entry.Size, Mode: int64(entry.Mode), ModifiedNs: entry.ModifiedNS,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) insertManifest(ctx context.Context, manifest rootManifest, chunks []manifestChunk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	inserted, err := q.InsertSourceManifest(ctx, db.InsertSourceManifestParams{
		ID: manifest.id, EntryCount: manifest.entryCount, TotalBytes: manifest.totalBytes,
	})
	if err != nil || inserted == 0 {
		return err
	}
	for _, chunk := range chunks {
		if err := q.InsertSourceManifestLayout(ctx, db.InsertSourceManifestLayoutParams{
			ManifestID: manifest.id, FirstPath: chunk.firstPath, ChunkID: chunk.id,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// buildFromStaging folds a survey's staged entries into a manifest a page
// at a time, so neither memory nor any one read spans the survey.
func (b *manifestBuilder) buildFromStaging(ctx context.Context, build string) (rootManifest, error) {
	after := ""
	for {
		rows, err := b.store.queries.ListSourceManifestStagingPage(ctx, db.ListSourceManifestStagingPageParams{
			BuildID: build, After: after, PageSize: stagingPage,
		})
		if err != nil {
			return rootManifest{}, err
		}
		for _, row := range rows {
			entry, err := entryFrom("", row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
			if err != nil {
				return rootManifest{}, err
			}
			if err := b.add(ctx, entry); err != nil {
				return rootManifest{}, err
			}
		}
		if len(rows) < stagingPage {
			return b.finish(ctx)
		}
		after = rows[len(rows)-1].Path
	}
}

// buildFromBase applies path overrides to a base manifest. It re-chunks
// from the first chunk an override touches and reuses base chunks again as
// soon as the stream returns to a cut the base also made, which yields the
// same chunks a full build of the new entries would. A nil override removes
// its path.
func (b *manifestBuilder) buildFromBase(ctx context.Context, base []manifestChunk, overrides map[string]*Entry) (rootManifest, error) {
	paths := make([]string, 0, len(overrides))
	for path := range overrides {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(base) == 0 {
		for _, path := range paths {
			if entry := overrides[path]; entry != nil {
				if err := b.add(ctx, *entry); err != nil {
					return rootManifest{}, err
				}
			}
		}
		return b.finish(ctx)
	}
	touched := make(map[int][]string)
	for _, path := range paths {
		i := sort.Search(len(base), func(i int) bool { return base[i].firstPath > path }) - 1
		touched[max(i, 0)] = append(touched[max(i, 0)], path)
	}
	for i, chunk := range base {
		changed := touched[i]
		if len(changed) == 0 && b.aligned() {
			b.reuse(chunk)
			continue
		}
		entries, err := b.store.chunkEntries(ctx, chunk.id, "")
		if err != nil {
			return rootManifest{}, err
		}
		if err := b.merge(ctx, entries, changed, overrides); err != nil {
			return rootManifest{}, err
		}
	}
	return b.finish(ctx)
}

// merge streams one base chunk with the overrides that fall in it.
func (b *manifestBuilder) merge(ctx context.Context, entries []Entry, changed []string, overrides map[string]*Entry) error {
	j := 0
	for _, entry := range entries {
		for j < len(changed) && changed[j] < entry.Path {
			if err := b.addOverride(ctx, overrides[changed[j]]); err != nil {
				return err
			}
			j++
		}
		if j < len(changed) && changed[j] == entry.Path {
			if err := b.addOverride(ctx, overrides[changed[j]]); err != nil {
				return err
			}
			j++
			continue
		}
		if err := b.add(ctx, entry); err != nil {
			return err
		}
	}
	for ; j < len(changed); j++ {
		if err := b.addOverride(ctx, overrides[changed[j]]); err != nil {
			return err
		}
	}
	return nil
}

func (b *manifestBuilder) addOverride(ctx context.Context, entry *Entry) error {
	if entry == nil {
		return nil
	}
	return b.add(ctx, *entry)
}

func chunkHash(entries []Entry) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("painted-wolf-source-manifest-chunk-v3\x00"))
	for _, entry := range entries {
		_, _ = hash.Write([]byte(entry.Path + "\x00" + entry.contentKey() + "\x00"))
		_, _ = hash.Write([]byte(strconv.FormatInt(entry.Size, 10) + "\x00" + strconv.FormatUint(uint64(entry.Mode), 10) + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func manifestHash(chunks []manifestChunk) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("painted-wolf-source-manifest-v1\x00"))
	for _, chunk := range chunks {
		_, _ = hash.Write([]byte(chunk.id + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func snapshotHash(roots []Root, manifests []string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("painted-wolf-source-snapshot-v3\x00"))
	for i, root := range roots {
		_, _ = hash.Write([]byte(root.Path + "\x00" + manifests[i] + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
