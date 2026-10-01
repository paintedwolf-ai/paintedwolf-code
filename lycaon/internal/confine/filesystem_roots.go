package confine

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// FilesystemRootForPath returns the narrowest process write root containing path.
// Callers enforce protected paths and worker isolation separately.
func FilesystemRootForPath(projectID string, roots, granted []string, sessionScratch, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", nil
	}
	allowed, err := validatedWriteRoots(projectID, roots, granted, sessionScratch)
	if err != nil {
		return "", err
	}
	abs := fspath.CanonicalPath(path)
	var best string
	for _, root := range allowed {
		rel, err := filepath.Rel(root, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && len(root) > len(best) {
			best = root
		}
	}
	return best, nil
}
