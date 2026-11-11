// Package fspath resolves filesystem path identity.
package fspath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalPath resolves aliases while preserving a missing path tail.
// It returns empty when link resolution exceeds its bound.
func CanonicalPath(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	// Relative paths retain their relative identity.
	if !filepath.IsAbs(cleaned) {
		return compareForm(cleaned)
	}
	var missing []string
	cur := cleaned
	links := 0
	for {
		if resolved, ok := canonicalizeExisting(cur); ok {
			out := resolved
			for i := len(missing) - 1; i >= 0; i-- {
				out = filepath.Join(out, missing[i])
			}
			return compareForm(out)
		}
		// Resolve dangling links before appending the missing tail.
		if target, err := os.Readlink(cur); err == nil {
			links++
			if links > 40 {
				return ""
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			cur = filepath.Clean(target)
			continue
		}
		dir := filepath.Dir(cur)
		base := filepath.Base(cur)
		if dir == cur {
			return compareForm(cleaned)
		}
		missing = append(missing, base)
		cur = dir
	}
}

// CanonicalPathOrMissing is CanonicalPath for a decision that must not outlive a
// failed resolution: only a missing name is preserved. Anything else that stops
// resolution, such as a denied or non-directory component, refuses.
func CanonicalPathOrMissing(path string) (string, bool) {
	if _, err := filepath.EvalSymlinks(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	resolved := CanonicalPath(path)
	return resolved, resolved != ""
}

// compareForm normalizes separators without trimming the root.
func compareForm(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "/" {
		return path
	}
	return strings.TrimSuffix(path, "/")
}
