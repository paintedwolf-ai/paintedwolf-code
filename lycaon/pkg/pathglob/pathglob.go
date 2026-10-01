// Package pathglob defines the repo-relative glob vocabulary shared by task
// scopes, sandbox profiles, workspace snapshots, and overlay promotion.
package pathglob

import (
	"path/filepath"
	"strings"
)

// Match reports whether relPath matches pattern. Patterns use slash-separated
// POSIX segments; ** matches zero or more segments, except a trailing ** must
// consume at least one segment.
func Match(pattern, relPath string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	if pattern == "" {
		return false
	}
	if pattern == "**" {
		return true
	}
	var pathSegments []string
	if relPath != "" {
		pathSegments = strings.Split(relPath, "/")
	}
	return matchSegments(strings.Split(pattern, "/"), pathSegments)
}

// Covers reports task-scope coverage. A non-glob path names both itself and
// its descendants; a glob uses Match exactly.
func Covers(pattern, relPath string) bool {
	pattern = normalizePattern(pattern)
	relPath = NormalizeRel(relPath)
	if pattern == "" || relPath == "" {
		return false
	}
	if pattern == "." {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		return Match(pattern, relPath)
	}
	return relPath == pattern || strings.HasPrefix(relPath, pattern+"/")
}

// NormalizeRel returns a clean slash-form relative path. Paths that are empty,
// name the root, or escape it return empty.
func NormalizeRel(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	return value
}

func normalizePattern(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "." || value == "./" {
		return "."
	}
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	if value == "" || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	return value
}

func matchSegments(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	segment := pattern[0]
	if segment == "**" {
		if len(pattern) == 1 {
			return len(path) >= 1
		}
		for consumed := 0; consumed <= len(path); consumed++ {
			if matchSegments(pattern[1:], path[consumed:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(translatePOSIXClass(segment), path[0])
	return err == nil && ok && matchSegments(pattern[1:], path[1:])
}

func translatePOSIXClass(pattern string) string {
	if !strings.ContainsRune(pattern, '[') {
		return pattern
	}
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			out.WriteByte(pattern[i])
			i++
			out.WriteByte(pattern[i])
			continue
		}
		out.WriteByte(pattern[i])
		if pattern[i] == '[' && i+1 < len(pattern) && pattern[i+1] == '!' {
			i++
			out.WriteByte('^')
		}
	}
	return out.String()
}
