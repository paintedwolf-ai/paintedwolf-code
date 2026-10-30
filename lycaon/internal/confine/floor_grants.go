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

// Grants returns the distinct recovery grants for refused access paths.
// Read metadata refusals on ancestor directories are satisfied by granting a
// refused descendant, and directory grants cover descendants only within the
// same floor layer.
func (s SandboxRefusals) Grants(access FilesystemAccess) []string {
	paths := s.Paths(access)
	candidates := make([]BoundaryPath, 0, len(paths))
	for i, path := range paths {
		if path.Grant == "" {
			continue
		}
		if access == AccessRead && metadataOnly(path.Operations) && hasGrantableDescendant(paths, i) {
			continue
		}
		candidates = append(candidates, path)
	}
	out := make([]string, 0, len(candidates))
	for i, candidate := range candidates {
		covered := false
		for j, other := range candidates {
			if i == j || other.Layer != candidate.Layer || PathEqual(other.Grant, candidate.Grant) {
				continue
			}
			if isDir(other.Grant) && PathAtOrUnder(candidate.Grant, other.Grant) {
				covered = true
				break
			}
		}
		if !covered && !slices.Contains(out, candidate.Grant) {
			out = append(out, candidate.Grant)
		}
	}
	return out
}

func metadataOnly(operations []string) bool {
	if len(operations) == 0 {
		return false
	}
	for _, op := range operations {
		if op != "file-read-metadata" {
			return false
		}
	}
	return true
}

func hasGrantableDescendant(paths []BoundaryPath, index int) bool {
	ancestor := paths[index].Path
	for i, other := range paths {
		if i != index && other.Grant != "" && PathStrictlyUnder(other.Path, ancestor) {
			return true
		}
	}
	return false
}

// repositoryTreeGrant returns the nearest work tree holding path that a
// reviewed write root may name, or the containing directory. The search runs
// on the resolved path through the filesystem root. Only a real .git directory or file marks a work tree, so a
// symlinked marker cannot stand in for one. The control-plane and protected
// floors stay in force inside the granted tree.
func repositoryTreeGrant(path string) string {
	containing := filepath.Dir(path)
	for dir := fspath.CanonicalPath(containing); ; dir = filepath.Dir(dir) {
		if !workTreeMarker(filepath.Join(dir, ".git")) {
			if filepath.Dir(dir) == dir {
				break
			}
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
