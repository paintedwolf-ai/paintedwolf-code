package confine

import (
	"github.com/lycaon/lycaon/internal/fspath"
	"path/filepath"
)

// FilesystemRootForPath returns the process write root containing path.
func FilesystemRootForPath(projectID string, roots, granted []string, sessionScratch, path string) (string, error) {
	return filesystemRootForPath(projectID, roots, granted, sessionScratch, path, osDefaultWriteRoots())
}

// NativeFilesystemRootForPath preserves default read scope while limiting writes to reviewed and session roots.
func NativeFilesystemRootForPath(projectID string, roots, granted []string, sessionScratch, path string, write bool) (string, error) {
	var defaults []string
	if !write {
		defaults = append(osDefaultWriteRoots(), SharedTempLocations()...)
	}
	return filesystemRootForPath(projectID, roots, granted, sessionScratch, path, defaults)
}

func filesystemRootForPath(projectID string, roots, granted []string, sessionScratch, path string, defaults []string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", nil
	}
	allowed, err := filesystemRoots(projectID, roots, granted, sessionScratch, defaults)
	if err != nil {
		return "", err
	}
	abs := fspath.CanonicalPath(path)
	var best string
	for _, root := range allowed {
		if len(root) > len(best) && PathAtOrUnder(abs, root) {
			best = root
		}
	}
	return best, nil
}
