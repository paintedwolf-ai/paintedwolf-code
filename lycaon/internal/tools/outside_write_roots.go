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

// OutsideWriteRoots keeps non-overlapping recordable folders in the
// confinement profile, session scratch included. A grant of a whole shared
// temp location is never compared, since other programs write there too; it
// is returned in unobserved instead.
func (tc ToolContext) OutsideWriteRoots() (roots []OutsideWriteRoot, unobserved []string) {
	if tc.Source.OutsideRoot.ID == "" {
		return nil, nil
	}
	attached := make([]string, 0, len(tc.Source.Roots))
	for _, root := range tc.Source.Roots {
		attached = append(attached, root.Path)
	}
	profile, err := confine.WriteRootsForBoundary(tc.Identity.ProjectID, attached, tc.Files.GrantedWriteRoots, tc.Host.SessionScratchDir)
	if err != nil {
		return nil, nil
	}
	temps := confine.SharedTempLocations()
	var candidates []string
	for _, root := range profile {
		if _, _, ok := tc.outsideLocation(root); !ok {
			continue
		}
		canonical := fspath.CanonicalPath(root)
		if canonical == "" {
			continue
		}
		canonical = filepath.Clean(canonical)
		if slices.ContainsFunc(temps, func(temp string) bool { return confine.PathEqual(canonical, temp) }) {
			if !slices.Contains(unobserved, canonical) {
				unobserved = append(unobserved, canonical)
			}
			continue
		}
		candidates = append(candidates, canonical)
	}
	sort.Strings(candidates)
	sort.Strings(unobserved)
	unrecorded := append(tc.unrecordedRoots(), temps...)
	for _, path := range candidates {
		if slices.ContainsFunc(roots, func(kept OutsideWriteRoot) bool { return confine.PathAtOrUnder(path, kept.Path) }) {
			continue
		}
		root := OutsideWriteRoot{Path: path}
		for _, folder := range unrecorded {
			canonical := fspath.CanonicalPath(folder)
			if canonical != "" && !confine.PathEqual(canonical, path) && confine.PathAtOrUnder(canonical, path) {
				root.Omit = append(root.Omit, canonical)
			}
		}
		roots = append(roots, root)
	}
	return roots, unobserved
}
