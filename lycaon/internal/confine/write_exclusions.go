package confine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/lycaon/lycaon/internal/fspath"
)

// WriteExclusionUnavailableError reports an exclusion the kernel profile
// cannot enforce by path. The caller must refuse before launching a process.
type WriteExclusionUnavailableError struct{ Path, Reason string }

func (e *WriteExclusionUnavailableError) Error() string {
	return fmt.Sprintf("cannot protect %s from writes: %s", e.Path, e.Reason)
}

const exclusionAncestorRemovalUnsupported = "ancestor_removal_unsupported"

func ValidateWriteExclusions(paths []string) error {
	// Read-only leaf mounts cannot prevent a writable ancestor from moving.
	if runtime.GOOS == "linux" && len(paths) > 0 {
		return &WriteExclusionUnavailableError{Path: paths[0], Reason: exclusionAncestorRemovalUnsupported}
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode().IsRegular() && exclusionHasAliases(info) {
			return &WriteExclusionUnavailableError{Path: path, Reason: "hard_link_aliases"}
		}
	}
	return nil
}

// writeExclusions guards the leaf and ancestor removal so a renamed directory
// cannot bypass the exclusion. Sibling writes keep their existing grants.
func writeExclusions(rules *FilesystemRules, paths []string) error {
	protected := make([]string, 0, 2*len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		parent, target := fspath.CanonicalPath(filepath.Dir(path)), fspath.CanonicalPath(path)
		if parent == "" || target == "" || !filepath.IsAbs(parent) || !filepath.IsAbs(target) {
			return &WriteExclusionUnavailableError{Path: path, Reason: "path_resolution_unavailable"}
		}
		// Preserve the declared leaf's namespace as well as the resolved object.
		for _, candidate := range []string{filepath.Join(parent, filepath.Base(path)), target} {
			if candidate != "" && !seen[candidate] {
				protected = append(protected, candidate)
				seen[candidate] = true
			}
		}
	}
	paths = protected
	if err := ValidateWriteExclusions(paths); err != nil {
		return err
	}
	rules.add(fsRule{op: opWrite, matches: subpathMatches(paths, FloorWriteConfigured)})
	ancestors := map[string]bool{}
	for _, path := range paths {
		for p := filepath.Dir(path); p != "" && p != "."; p = filepath.Dir(p) {
			ancestors[p] = true
			if filepath.Dir(p) == p {
				break
			}
		}
	}
	ordered := make([]string, 0, len(ancestors))
	for p := range ancestors {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	rules.add(fsRule{op: opWriteRemove, matches: literalMatches(ordered, FloorWriteConfigured)})
	return nil
}
