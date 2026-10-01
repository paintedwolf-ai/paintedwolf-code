package confine

import (
	"os"
	"path/filepath"
	"slices"
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
