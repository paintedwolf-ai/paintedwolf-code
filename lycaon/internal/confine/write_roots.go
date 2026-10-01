package confine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// WriteRootsForProject returns the compiled write-root union.
func WriteRootsForProject(projectID string, projectRoots []string) []string {
	return WriteRootsForBoundary(projectID, projectRoots, nil, "")
}

// PathWithinWriteRoots matches existing and not-yet-created paths.
func PathWithinWriteRoots(path string, writeRoots []string) bool {
	p := fspath.CanonicalPath(path)
	if p == "" {
		return false
	}
	for _, root := range writeRoots {
		r := fspath.CanonicalPath(root)
		if r == "" {
			continue
		}
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

func resolveArgPath(arg, projectDir string) (string, bool) {
	p := arg
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		if arg == "~" {
			p = home
		} else {
			p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	abs := strings.HasPrefix(p, "/") || isWindowsAbsPath(p)
	if !abs {
		if projectDir == "" {
			return "", false
		}
		p = filepath.Join(projectDir, p)
	}
	p = fspath.CanonicalPath(p)
	return p, p != ""
}

func isWindowsAbsPath(path string) bool {
	if len(path) < 2 {
		return false
	}
	return path[1] == ':'
}
