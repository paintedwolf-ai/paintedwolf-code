package project

import (
	"path/filepath"
	"strings"
)

// OverlayCacheKey canonicalizes overlay root paths for cache keys.
func OverlayCacheKey(rootPaths []string) string {
	if len(rootPaths) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rootPaths))
	for _, p := range rootPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		parts = append(parts, filepath.Clean(p))
	}
	return strings.Join(parts, "\x00")
}
