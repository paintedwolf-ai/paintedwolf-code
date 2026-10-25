package sourcesnapshot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
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

// rootCandidate is one root's next generation while it converges. A survey
// stages every admitted entry; a delta holds the covered manifest and only
// the paths that changed since.
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

	base       string
	baseLayout []manifestChunk
	// A nil override removes a path from the base.
	overrides map[string]*Entry

	// pin holds the base and every chunk and manifest this candidate wrote.
	pin   *pin
	built *rootManifest
}

func (c *rootCandidate) isDelta() bool { return c.base != "" }

func (c *rootCandidate) unstableFiles() bool { return len(c.unstable) > 0 }

func (c *rootCandidate) put(ctx context.Context, s *Store, entry Entry) error {
	c.built = nil
	if c.isDelta() {
		held := entry
		c.overrides[entry.Path] = &held
		return nil
	}
	c.pending = append(c.pending, db.InsertSourceManifestStagingParams{
		BuildID: c.build, Path: entry.Path,
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
	c.built = nil
	for p := range c.boundaries {
		if p == rel || underDir(p, rel) {
			delete(c.boundaries, p)
		}
	}
	if !c.isDelta() {
		return c.dropStaged(ctx, s, rel)
	}
	for p := range c.overrides {
		if p == rel || underDir(p, rel) {
			delete(c.overrides, p)
		}
	}
	var held []string
	err := s.manifestEntriesUnder(ctx, c.base, c.root.Path, rel, func(entry Entry) error {
		c.overrides[entry.Path] = nil
		held = append(held, entry.Path)
		return nil
	})
	if err != nil {
		return err
	}
	return s.forgetPaths(ctx, c.root.Path, held)
}

func (c *rootCandidate) dropStaged(ctx context.Context, s *Store, rel string) error {
	if err := c.flushStaging(ctx, s); err != nil {
		return err
	}
	var held []string
	exact, err := s.queries.SourceManifestStagingHoldsPath(ctx, db.SourceManifestStagingHoldsPathParams{
		BuildID: c.build, Path: rel,
	})
	if err != nil {
		return err
	}
	if exact != 0 {
		held = append(held, rel)
		if err := s.queries.DeleteSourceManifestStagingPath(ctx, db.DeleteSourceManifestStagingPathParams{
			BuildID: c.build, Path: rel,
		}); err != nil {
			return err
		}
	}
	low, high := pathBounds(rel)
	below, err := s.queries.ListSourceManifestStagingPathsBelow(ctx, db.ListSourceManifestStagingPathsBelowParams{
		BuildID: c.build, Low: low, High: high,
	})
	if err != nil {
		return err
	}
	held = append(held, below...)
	if err := s.queries.DeleteSourceManifestStagingBelow(ctx, db.DeleteSourceManifestStagingBelowParams{
		BuildID: c.build, Low: low, High: high,
	}); err != nil {
		return err
	}
	return s.forgetPaths(ctx, c.root.Path, held)
}

// manifest builds the candidate's generation once per change and writes its
// chunks; a second call without changes returns the same manifest.
func (c *rootCandidate) manifest(ctx context.Context, s *Store) (rootManifest, error) {
	if c.built != nil {
		return *c.built, nil
	}
	if err := c.flushStaging(ctx, s); err != nil {
		return rootManifest{}, err
	}
	builder := s.newManifestBuilder(c.pin)
	var (
		out rootManifest
		err error
	)
	if c.isDelta() {
		out, err = builder.buildFromBase(ctx, c.baseLayout, c.overrides)
	} else {
		out, err = builder.buildFromStaging(ctx, c.build)
	}
	if err != nil {
		return rootManifest{}, err
	}
	c.built = &out
	return out, nil
}

// close releases staging rows, pins, and the cache reader. It runs after
// the request may have ended, so its own failures are logged, not returned.
func (c *rootCandidate) close(ctx context.Context, s *Store) {
	if c == nil {
		return
	}
	c.baseline.close()
	c.pin.release()
	if c.build != "" {
		if err := s.releaseBuild(ctx, c.build); err != nil {
			slog.WarnContext(ctx, "source snapshot staging release", "root", c.root.Path, "error", err)
		}
		c.build = ""
	}
}

// releaseBuild deletes one build's staging a page at a time.
func (s *Store) releaseBuild(ctx context.Context, build string) error {
	for {
		removed, err := s.queries.DeleteSourceManifestStagingPage(ctx, db.DeleteSourceManifestStagingPageParams{
			BuildID: build, PageSize: stagingPage,
		})
		if err != nil || removed == 0 {
			return err
		}
	}
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
