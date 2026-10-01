package confine

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/fspath"
)

// loaderTreeGrant expands missing paths to an existing loader directory or its root.
func loaderTreeGrant(path string, roots []string) string {
	classify := AgentPolicyClassifier(roots...)
	location, ok := classify(path)
	if !ok || location.Dir == "" || isDir(filepath.Dir(path)) {
		return path
	}
	top := path
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if within, inside := classify(dir); !inside || within != location {
			break
		}
		if isDir(dir) {
			return dir
		}
		top = dir
	}
	return top
}

// CoveringGrants drops each grant an existing directory grant already covers.
func CoveringGrants(grants []string) []string {
	out := make([]string, 0, len(grants))
	for i, grant := range grants {
		covered := false
		for j, other := range grants {
			if i == j || other == grant || !isDir(other) || !PathAtOrUnder(grant, other) {
				continue
			}
			covered = true
			break
		}
		if !covered && !slices.Contains(out, grant) {
			out = append(out, grant)
		}
	}
	return out
}

// repositoryTreeGrant returns the nearest work tree holding path that a
// reviewed write root may name, or the containing directory. The search runs
// on the resolved path and stops below the home directory and top-level
// directories. Only a real .git directory or file marks a work tree, so a
// symlinked marker cannot stand in for one. The control-plane and protected
// floors stay in force inside the granted tree.
func repositoryTreeGrant(path string) string {
	containing := filepath.Dir(path)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return containing
	}
	home = fspath.CanonicalPath(home)
	for dir := fspath.CanonicalPath(containing); !topLevelDir(dir) && !PathEqual(dir, home); dir = filepath.Dir(dir) {
		if !workTreeMarker(filepath.Join(dir, ".git")) {
			continue
		}
		if refused, _ := GrantedWriteRootRefused(dir); refused {
			return containing
		}
		return dir
	}
	return containing
}

func workTreeMarker(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

// topLevelDir reports the filesystem root and its direct children.
func topLevelDir(path string) bool {
	parent := filepath.Dir(path)
	return parent == path || filepath.Dir(parent) == parent
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
