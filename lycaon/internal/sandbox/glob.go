package sandbox

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/pkg/pathglob"
)

// matchAnyGlob treats an empty pattern set as match-all.
func matchAnyGlob(patterns []string, relPath string) bool {
	if len(patterns) == 0 {
		return true
	}
	relPath = filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	for _, pattern := range patterns {
		if pathglob.Match(pattern, relPath) {
			return true
		}
	}
	return false
}

// matchesScopeGlob treats an empty pattern set as no match.
func matchesScopeGlob(patterns []string, relPath string) bool {
	if len(patterns) == 0 {
		return false
	}
	relPath = filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	for _, pattern := range patterns {
		if pathglob.Match(pattern, relPath) {
			return true
		}
	}
	return false
}

// matchToolPattern supports exact names and trailing-* prefixes.
func matchToolPattern(pattern, toolName string) bool {
	if pattern == toolName {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(toolName, prefix)
	}
	return false
}
