package workspace

import (
	"context"
	"io/fs"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// catalogSourceIndex answers consumer inventory from the host's source catalog, so a worker view
// reuses its identity and freshness instead of keeping a second cache. A generation the catalog has
// not finished walking is reported as partial coverage, never as proof that a path is absent.
type catalogSourceIndex struct {
	projectID string
	roots     []sourcecatalog.Root
}

func newCatalogSourceIndex(projectID string, roots []adapter.PreparedRoot) *catalogSourceIndex {
	index := &catalogSourceIndex{projectID: projectID}
	for _, r := range roots {
		index.roots = append(index.roots, sourcecatalog.Root{ID: r.RootID, Path: r.SourcePath})
	}
	return index
}

// QueryIndex implements consumer.SourceIndexProvider.
func (c *catalogSourceIndex) QueryIndex(ctx context.Context, q consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
	snapshot := sourcecatalog.Process().Current(ctx, c.projectID, c.roots)
	observedAt := time.Now().UTC()
	coverage := catalogCoverage(snapshot)
	result := consumer.SourceIndexResult{Coverage: coverage, ObservedAt: observedAt, SourceGeneration: snapshot.Revision}
	wanted := make(map[string]bool, len(q.RootIDs))
	for _, id := range q.RootIDs {
		wanted[id] = true
	}
	prefix := strings.Trim(q.PathPrefix, "/")
	freshness := consumer.IndexFreshness{ObservedAt: observedAt, Coverage: coverage, Stale: coverage == consumer.CoverageStale, SourceGeneration: snapshot.Revision}
	for _, root := range c.roots {
		if len(wanted) > 0 && !wanted[root.ID] {
			continue
		}
		dir := prefix
		if subroot := strings.Trim(q.Subroots[root.ID], "/"); subroot != "" && dir == "" {
			dir = subroot
		}
		err := snapshot.Walk(ctx, root.ID, dir, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
			if q.MaxResults > 0 && len(result.Entries) >= q.MaxResults {
				result.Truncated = true
				return sourcecatalog.WalkStop
			}
			result.Entries = append(result.Entries, catalogIndexEntry(entry, observedAt, freshness))
			return sourcecatalog.WalkContinue
		})
		if err != nil {
			return consumer.SourceIndexResult{}, err
		}
	}
	return result, nil
}

func catalogIndexEntry(entry sourcecatalog.Entry, observedAt time.Time, freshness consumer.IndexFreshness) consumer.SourceIndexEntry {
	kind := layeredfs.EntryRegular
	switch {
	case entry.IsSymlink:
		kind = layeredfs.EntrySymlink
	case entry.IsDir:
		kind = layeredfs.EntryDirectory
	}
	return consumer.SourceIndexEntry{
		Path: layeredfs.QualifiedPath{RootID: entry.RootID, Rel: entry.Path},
		Kind: kind, Mode: fs.FileMode(entry.Mode).Perm(), Size: entry.Size, ModTime: entry.Modified,
		ObservedAt: observedAt, Freshness: freshness,
	}
}

func catalogCoverage(snapshot sourcecatalog.Snapshot) consumer.IndexCoverage {
	switch {
	case snapshot.State != sourcecatalog.StateReady:
		if len(snapshot.Entries) == 0 {
			return consumer.CoverageAbsent
		}
		return consumer.CoveragePartial
	case snapshot.RootBoundary:
		return consumer.CoveragePartial
	case snapshot.Refreshing || snapshot.Moving:
		return consumer.CoverageStale
	default:
		return consumer.CoverageComplete
	}
}
