package sourcesnapshot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcescope"
)

// stagingBatch bounds entries buffered before a staging write.
const stagingBatch = 2048

// The process prefix separates abandoned builds from active builds.
var buildPrefix = func() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}()

var buildSerial atomic.Uint64

func newBuildID() string {
	return buildPrefix + "-" + strconv.FormatUint(buildSerial.Add(1), 10)
}

// Surveys stage entries by bucket; delta candidates retain only changed paths.
type rootCandidate struct {
	root       Root
	scope      *sourcescope.Scope
	baseline   *observed
	ids        *identifier
	boundaries map[string]Boundary
	unstable   []string
	// Surveys without watcher coverage need a confirming pass.
	surveyed  bool
	confirmed bool

	build   string
	pending []db.InsertSourceManifestStagingParams

	// Overrides apply to the base generation's chunks.
	base       string
	baseChunks map[byte]string
	// A nil override removes a path from the base.
	overrides map[byte]map[string]*Entry
}

func bucketOf(rootPath, rel string) byte {
	sum := sha256.Sum256([]byte(rootPath + "\x00" + rel))
	return sum[0]
}

func (c *rootCandidate) isDelta() bool { return c.base != "" }

func (c *rootCandidate) unstableFiles() bool { return len(c.unstable) > 0 }

func (c *rootCandidate) put(ctx context.Context, s *Store, entry Entry) error {
	bucket := bucketOf(entry.RootPath, entry.Path)
	if c.isDelta() {
		if c.overrides[bucket] == nil {
			c.overrides[bucket] = make(map[string]*Entry)
		}
		held := entry
		c.overrides[bucket][entry.Path] = &held
		return nil
	}
	c.pending = append(c.pending, db.InsertSourceManifestStagingParams{
		BuildID: c.build, Bucket: int64(bucket), RootPath: entry.RootPath, Path: entry.Path,
		Sha256: entry.SHA256, GitOid: entry.GitOID, Identity: string(entry.Identity),
		Size: entry.Size, Mode: int64(entry.Mode), ModifiedNs: entry.ModifiedNS,
	})
	if len(c.pending) >= stagingBatch {
		return c.flushStaging(ctx, s)
	}
	return nil
}

func (c *rootCandidate) flushStaging(ctx context.Context, s *Store) error {
	if len(c.pending) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	for _, row := range c.pending {
		if err := q.InsertSourceManifestStaging(ctx, row); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.pending = c.pending[:0]
	return nil
}

// Removed subtrees also lose their cached file identities.
func (c *rootCandidate) drop(ctx context.Context, s *Store, rel string) error {
	for p := range c.boundaries {
		if p == rel || underDir(p, rel) {
			delete(c.boundaries, p)
		}
	}
	low, high := pathBounds(rel)
	if !c.isDelta() {
		if err := c.flushStaging(ctx, s); err != nil {
			return err
		}
		var held []string
		exact, err := s.queries.SourceManifestStagingHoldsPath(ctx, db.SourceManifestStagingHoldsPathParams{
			BuildID: c.build, RootPath: c.root.Path, Path: rel,
		})
		if err != nil {
			return err
		}
		if exact != 0 {
			held = append(held, rel)
			if err := s.queries.DeleteSourceManifestStagingPath(ctx, db.DeleteSourceManifestStagingPathParams{
				BuildID: c.build, RootPath: c.root.Path, Path: rel,
			}); err != nil {
				return err
			}
		}
		below, err := s.queries.ListSourceManifestStagingPathsBelow(ctx, db.ListSourceManifestStagingPathsBelowParams{
			BuildID: c.build, RootPath: c.root.Path, Low: low, High: high,
		})
		if err != nil {
			return err
		}
		held = append(held, below...)
		if err := s.queries.DeleteSourceManifestStagingBelow(ctx, db.DeleteSourceManifestStagingBelowParams{
			BuildID: c.build, RootPath: c.root.Path, Low: low, High: high,
		}); err != nil {
			return err
		}
		return s.forgetPaths(ctx, c.root.Path, held)
	}
	for bucket, paths := range c.overrides {
		for p := range paths {
			if p == rel || underDir(p, rel) {
				delete(paths, p)
			}
		}
		if len(paths) == 0 {
			delete(c.overrides, bucket)
		}
	}
	gone, err := s.EntriesUnder(ctx, c.base, c.root.Path, rel)
	if err != nil {
		return err
	}
	held := make([]string, 0, len(gone))
	for _, entry := range gone {
		bucket := bucketOf(entry.RootPath, entry.Path)
		if c.overrides[bucket] == nil {
			c.overrides[bucket] = make(map[string]*Entry)
		}
		c.overrides[bucket][entry.Path] = nil
		held = append(held, entry.Path)
	}
	return s.forgetPaths(ctx, c.root.Path, held)
}

func (c *rootCandidate) touched(bucket byte) bool {
	return !c.isDelta() || c.overrides[bucket] != nil
}

func (c *rootCandidate) bucketEntries(ctx context.Context, q *db.Queries, bucket byte) ([]Entry, error) {
	if !c.isDelta() {
		rows, err := q.ListSourceManifestStagingBucket(ctx, db.ListSourceManifestStagingBucketParams{
			BuildID: c.build, Bucket: int64(bucket),
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
	var out []Entry
	if chunkID, ok := c.baseChunks[bucket]; ok {
		rows, err := q.ListSourceManifestChunkEntries(ctx, chunkID)
		if err != nil {
			return nil, err
		}
		overrides := c.overrides[bucket]
		for _, row := range rows {
			if row.RootPath != c.root.Path {
				continue
			}
			if _, overridden := overrides[row.Path]; overridden {
				continue
			}
			entry, err := entryFrom(row.RootPath, row.Path, row.Sha256, row.GitOid, row.Identity, row.Size, row.Mode, row.ModifiedNs)
			if err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
	}
	for _, entry := range c.overrides[bucket] {
		if entry != nil {
			out = append(out, *entry)
		}
	}
	return out, nil
}

// Bucket hashes compare successive surveys.
func (c *rootCandidate) chunkIDs(ctx context.Context, s *Store) (map[byte]string, error) {
	if err := c.flushStaging(ctx, s); err != nil {
		return nil, err
	}
	out := make(map[byte]string)
	for bucket := range 256 {
		if !c.touched(byte(bucket)) {
			if id, ok := c.baseChunks[byte(bucket)]; ok {
				out[byte(bucket)] = id
			}
			continue
		}
		entries, err := c.bucketEntries(ctx, s.queries, byte(bucket))
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			continue
		}
		sortEntries(entries)
		out[byte(bucket)] = manifestChunkHash(byte(bucket), entries)
	}
	return out, nil
}

// Closing releases staging rows and the cache reader.
func (c *rootCandidate) close(ctx context.Context, s *Store) {
	if c == nil {
		return
	}
	c.baseline.close()
	if c.build != "" {
		for bucket := range 256 {
			_ = s.queries.DeleteSourceManifestStagingBucket(ctx, db.DeleteSourceManifestStagingBucketParams{
				BuildID: c.build, Bucket: int64(bucket),
			})
		}
		c.build = ""
	}
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].RootPath != entries[j].RootPath {
			return entries[i].RootPath < entries[j].RootPath
		}
		return entries[i].Path < entries[j].Path
	})
}

func entryFrom(rootPath, path, sha256Hex, gitOID, identity string, size, mode, modifiedNS int64) (Entry, error) {
	fileMode, err := fileModeFromDB(mode)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		RootPath: rootPath, Path: path, SHA256: sha256Hex, GitOID: gitOID, Identity: Identity(identity),
		Size: size, Mode: fileMode, ModifiedNS: modifiedNS,
	}, nil
}
