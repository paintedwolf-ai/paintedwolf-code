package sourceledger

import (
	"path"
	"sort"

	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

type outsideComparison struct {
	start, end            sourcesnapshot.Snapshot
	beforeGaps, afterGaps map[string]bool
}

func boundaryPaths(boundaries []sourcesnapshot.Boundary) map[string]bool {
	if len(boundaries) == 0 {
		return nil
	}
	paths := make(map[string]bool, len(boundaries))
	for _, boundary := range boundaries {
		paths[boundary.Path] = true
	}
	return paths
}

// An entry missing behind a boundary does not establish creation or deletion.
func (c outsideComparison) observed(change sourcesnapshot.Change) bool {
	switch {
	case change.Before == nil:
		return !underBoundary(change.Path(), c.beforeGaps)
	case change.After == nil:
		return !underBoundary(change.Path(), c.afterGaps)
	default:
		return true
	}
}

func underBoundary(rel string, paths map[string]bool) bool {
	if len(paths) == 0 {
		return false
	}
	for {
		if paths[rel] {
			return true
		}
		if rel == "." {
			return false
		}
		rel = path.Dir(rel)
	}
}

func (c outsideComparison) boundaries() []sourcesnapshot.Boundary {
	unique := make(map[[2]string]bool, len(c.beforeGaps)+len(c.afterGaps))
	var out []sourcesnapshot.Boundary
	for _, side := range [][]sourcesnapshot.Boundary{c.start.Boundaries, c.end.Boundaries} {
		for _, boundary := range side {
			if boundary.Reason == "scope" && boundary.Detail == sourcescope.ReasonOmitted {
				continue
			}
			key := [2]string{boundary.Path, boundary.Reason}
			if !unique[key] {
				unique[key] = true
				out = append(out, boundary)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
