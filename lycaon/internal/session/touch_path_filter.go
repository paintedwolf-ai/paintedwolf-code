package session

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/oswalk"
	"github.com/lycaon/lycaon/pkg/pathglob"
)

const touchPathWalkMaxFiles = 500

// FilterTouchPathsForProject keeps manifest touch.paths that apply to the open project directory.
func FilterTouchPathsForProject(projectDir string, paths []string) []string {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" || len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, pattern := range paths {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if TouchPathAppliesToProject(projectDir, pattern) {
			out = append(out, pattern)
		}
	}
	return out
}

// TouchPathAppliesToProject reports whether pattern matches at least one path under projectDir.
func TouchPathAppliesToProject(projectDir, pattern string) bool {
	projectDir = strings.TrimSpace(projectDir)
	pattern = strings.TrimSpace(pattern)
	if projectDir == "" || pattern == "" {
		return false
	}
	if pattern == "**" || pattern == "*" || pattern == "**/*" {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if prefix == "" {
			return true
		}
		return pathExists(filepath.Join(projectDir, filepath.FromSlash(prefix)))
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return pathExists(filepath.Join(projectDir, filepath.FromSlash(pattern)))
	}
	return projectHasPathGlobMatch(projectDir, pattern)
}

func pathExists(abs string) bool {
	_, err := os.Stat(abs)
	return err == nil
}

func projectHasPathGlobMatch(projectDir, pattern string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	found := false
	seen := 0
	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return oswalk.Skip(err)
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(projectDir, path)
		if relErr != nil {
			return oswalk.Skip(relErr)
		}
		if pathglob.Match(pattern, filepath.ToSlash(rel)) {
			found = true
			return fs.SkipAll
		}
		seen++
		if seen >= touchPathWalkMaxFiles {
			return fs.SkipAll
		}
		return nil
	})
	return found
}
