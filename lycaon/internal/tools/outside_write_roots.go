package tools

import (
	"path/filepath"
	"slices"
	"sort"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
)

// OutsideWriteRoot pairs a granted folder with its excluded subtrees.
type OutsideWriteRoot struct {
	Path string
	Omit []string
}

// OutsideWriteRoots keeps non-overlapping recordable folders in the confinement profile.
func (tc ToolContext) OutsideWriteRoots() []OutsideWriteRoot {
	if tc.Source.OutsideRoot.ID == "" {
		return nil
	}
	attached := make([]string, 0, len(tc.Source.Roots))
	for _, root := range tc.Source.Roots {
		attached = append(attached, root.Path)
	}
	profile, err := confine.WriteRootsForBoundary(tc.Identity.ProjectID, attached, tc.Files.GrantedWriteRoots, tc.Host.SessionScratchDir)
	if err != nil {
		return nil
	}
	var candidates []string
	for _, root := range profile {
		if _, _, ok := tc.outsideLocation(root); !ok {
			continue
		}
		if canonical := fspath.CanonicalPath(root); canonical != "" {
			candidates = append(candidates, filepath.Clean(canonical))
		}
	}
	sort.Strings(candidates)
	unrecorded := tc.unrecordedRoots()
	var out []OutsideWriteRoot
	for _, path := range candidates {
		if slices.ContainsFunc(out, func(kept OutsideWriteRoot) bool { return confine.PathAtOrUnder(path, kept.Path) }) {
			continue
		}
		root := OutsideWriteRoot{Path: path}
		for _, folder := range unrecorded {
			canonical := fspath.CanonicalPath(folder)
			if canonical != "" && !confine.PathEqual(canonical, path) && confine.PathAtOrUnder(canonical, path) {
				root.Omit = append(root.Omit, canonical)
			}
		}
		out = append(out, root)
	}
	return out
}
