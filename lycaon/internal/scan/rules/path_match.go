package rules

import (
	"path/filepath"
	"strings"
)

// PathUnderExclude reports whether rel matches a configured directory pattern.
func PathUnderExclude(rel string, patterns []string) bool {
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	if rel == "" || len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		name := strings.Trim(strings.TrimSpace(pattern), "/")
		if name == "" || strings.ContainsAny(name, "*?") {
			continue
		}
		if strings.Contains(name, "/") {
			if rel == name || strings.HasPrefix(rel, name+"/") || strings.Contains(rel, "/"+name+"/") {
				return true
			}
			continue
		}
		for _, seg := range strings.Split(rel, "/") {
			if seg == name {
				return true
			}
		}
	}
	return false
}
