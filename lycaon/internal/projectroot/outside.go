package projectroot

import (
	"path/filepath"
	"runtime"
	"strings"
)

// OutsidePath is the folder the outside namespace's paths are relative to.
const OutsidePath = "/"

// OutsideLabel names the outside namespace wherever a root label is shown.
const OutsideLabel = "outside"

// OutsideRef is the reference for a project's outside namespace.
func OutsideRef(id string) RootRef {
	return RootRef{ID: id, Label: OutsideLabel, Path: OutsidePath, Outside: true}
}

// OutsideRel maps an absolute host path to its path in the outside namespace.
// Windows volumes have no single root to be relative to, so they have none.
func OutsideRel(abs string) (string, bool) {
	if runtime.GOOS == "windows" || !filepath.IsAbs(abs) {
		return "", false
	}
	rel := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(abs)), "/")
	return rel, rel != ""
}

// OutsideAbs is the host path of an outside-namespace path.
func OutsideAbs(rel string) string {
	return filepath.FromSlash("/" + strings.TrimPrefix(rel, "/"))
}
