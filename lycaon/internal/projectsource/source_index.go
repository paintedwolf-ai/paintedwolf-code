package projectsource

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// SourceIndexState describes whether an indexed source snapshot can answer queries.
type SourceIndexState string

const (
	SourceIndexWarming SourceIndexState = "warming"
	SourceIndexReady   SourceIndexState = "ready"
	SourceIndexFailed  SourceIndexState = "failed"
)

// SourceIndexEntry is one searchable file address.
type SourceIndexEntry struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
}

// SourceIndexResource is a well-known project document found while indexing.
type SourceIndexResource struct {
	Kind string
	Path string
}

// SourceIndexRoot summarizes one attached root without returning its file list.
type SourceIndexRoot struct {
	RootID    string
	FileCount int
	Resources []SourceIndexResource
}

type SourceIndexRootCoverage struct {
	RootID string
	State  SourceIndexState
	sourcecatalog.IndexCoverage
}

// SourceIndexSnapshot pins each root's persisted metadata generation.
type SourceIndexSnapshot struct {
	State      SourceIndexState
	Revision   uint64
	Refreshing bool
	FileCount  int
	Roots      []SourceIndexRoot
	Coverage   []SourceIndexRootCoverage
	readers    []sourceIndexReader
	// rootPaths lists every selected root, ready or not.
	rootPaths []string
}

type sourceIndexReader struct {
	rootID string
	// path is the root folder queries may name as part of a file's full path.
	path   string
	reader *sourcecatalog.IndexReader
}

func (s SourceIndexSnapshot) Close() {
	for _, root := range s.readers {
		_ = root.reader.Close()
	}
}

type SourceIndexCache struct{ catalog *sourcecatalog.Catalog }

func NewSourceIndexCache() *SourceIndexCache {
	return &SourceIndexCache{catalog: sourcecatalog.Process()}
}

// Files and quick open include hidden paths.
var sourceFileScope = sourcecatalog.FileScope{Audience: sourcecatalog.HumanAudience, IncludeHidden: true}

// Snapshot retains readable roots independently of roots still preparing or failed.
func (c *SourceIndexCache) Snapshot(ctx context.Context, p ProjectSource) SourceIndexSnapshot {
	if c == nil || p == nil || len(p.SourceRoots()) == 0 {
		return SourceIndexSnapshot{State: SourceIndexFailed}
	}
	out := SourceIndexSnapshot{State: SourceIndexFailed}
	revisions := sha256.New()
	for _, root := range orderedRootsForSource(p) {
		out.rootPaths = append(out.rootPaths, root.Path)
		reader, status, err := c.catalog.Trees.OpenIndex(ctx, p.SourceID(), sourcecatalog.Root{ID: root.ID, Path: root.Path}, 0)
		coverage := SourceIndexRootCoverage{RootID: root.ID, State: SourceIndexWarming, IndexCoverage: sourcecatalog.CoverageFromStatus(status)}
		if reader != nil && err == nil {
			coverage.IndexCoverage, err = reader.Coverage(ctx)
			if err == nil {
				var summary SourceIndexRoot
				summary, err = sourceRootSummary(ctx, root.ID, reader)
				if err == nil {
					coverage.State = SourceIndexReady
					out.readers = append(out.readers, sourceIndexReader{root.ID, root.Path, reader})
					out.FileCount += summary.FileCount
					out.Roots = append(out.Roots, summary)
				}
			}
		}
		if err != nil || status.State == sourcecatalog.StateFailed {
			coverage.State = SourceIndexFailed
			if err != nil {
				coverage.Error = err.Error()
			}
			if reader != nil {
				_ = reader.Close()
			}
		}
		out.Coverage = append(out.Coverage, coverage)
		out.Refreshing = out.Refreshing || coverage.Pending()
		fmt.Fprintf(revisions, "%s:%s:%d:%s:%t:%t:%d:%d:%s\x00", root.ID, root.Path, status.Revision, coverage.State,
			coverage.DiscoveryComplete, coverage.Refreshing, coverage.BoundedDirectories, coverage.FailedDirectories, coverage.Error)
	}
	if len(out.readers) > 0 {
		out.State = SourceIndexReady
	} else if out.Refreshing {
		out.State = SourceIndexWarming
	}
	// JavaScript numbers preserve all 53 revision bits.
	out.Revision = max(1, binary.BigEndian.Uint64(revisions.Sum(nil))>>11)
	return out
}

var sourceIndexResourceCandidates = map[string]string{
	".github/readme.md": "readme", "readme.md": "readme", "docs/readme.md": "readme",
	"agents.md": "agents", ".github/contributing.md": "contributing", "contributing.md": "contributing",
}

func sourceRootSummary(ctx context.Context, rootID string, reader *sourcecatalog.IndexReader) (SourceIndexRoot, error) {
	files, err := reader.FileCount(ctx, sourceFileScope)
	if err != nil {
		return SourceIndexRoot{}, err
	}
	names := make([]string, 0, len(sourceIndexResourceCandidates))
	for name := range sourceIndexResourceCandidates {
		names = append(names, name)
	}
	paths, err := reader.NamedFiles(ctx, sourceFileScope, names)
	if err != nil {
		return SourceIndexRoot{}, err
	}
	out := SourceIndexRoot{RootID: rootID, FileCount: files}
	seen := map[string]bool{}
	for _, path := range paths {
		kind := sourceIndexResourceCandidates[strings.ToLower(path)]
		if !seen[kind] {
			out.Resources = append(out.Resources, SourceIndexResource{Kind: kind, Path: path})
			seen[kind] = true
		}
	}
	return out, nil
}
