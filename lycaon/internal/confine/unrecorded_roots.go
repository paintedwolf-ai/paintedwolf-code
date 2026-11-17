package confine

import "github.com/lycaon/lycaon/internal/fspath"

// UnrecordedRoots are the host folders whose changes source history never
// records outside a project: temporary folders, caches, tool data and the
// host's own state.
func UnrecordedRoots() []string {
	roots := controlPlaneReadDenyRootsResolved()
	for _, root := range osDefaultWriteRoots() {
		if canonical := fspath.CanonicalPath(root); canonical != "" {
			roots = append(roots, canonical)
		}
	}
	return roots
}
