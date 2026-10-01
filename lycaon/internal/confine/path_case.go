package confine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// pathFoldCache memoizes case behavior by directory.
var pathFoldCache sync.Map // string -> bool

// FoldsCase reports whether path's filesystem folds filename case.
// Unknown behavior is treated as folded for protected paths.
func FoldsCase(path string) bool {
	dir := nearestExistingDir(path)
	if dir == "" {
		return true
	}
	if cached, ok := pathFoldCache.Load(dir); ok {
		return cached.(bool)
	}
	folds := volumeFoldsCase(dir)
	pathFoldCache.Store(dir, folds)
	return folds
}

// PathEqual reports whether a and b name the same location.
func PathEqual(a, b string) bool {
	x, y := normalizeForCompare(a), normalizeForCompare(b)
	if x == "" || y == "" {
		return false
	}
	if x == y {
		return true
	}
	return FoldsCase(b) && strings.EqualFold(x, y)
}

// PathAtOrUnder reports whether path is root or sits inside it.
func PathAtOrUnder(path, root string) bool {
	p, r := normalizeForCompare(path), normalizeForCompare(root)
	if p == "" || r == "" {
		return false
	}
	if atOrUnderExact(p, r) {
		return true
	}
	if !FoldsCase(root) {
		return false
	}
	return atOrUnderExact(strings.ToLower(p), strings.ToLower(r))
}

// PathStrictlyUnder reports whether path sits inside root without being root.
func PathStrictlyUnder(path, root string) bool {
	return PathAtOrUnder(path, root) && !PathEqual(path, root)
}

// NameContains applies the context filesystem's case behavior.
func NameContains(contextPath, haystack, needle string) bool {
	if strings.Contains(haystack, needle) {
		return true
	}
	if !FoldsCase(contextPath) {
		return false
	}
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// GlobMatchesName applies the context filesystem's case behavior.
func GlobMatchesName(contextPath, glob, base string) bool {
	if ok, err := filepath.Match(glob, base); err == nil && ok {
		return true
	}
	if !FoldsCase(contextPath) {
		return false
	}
	ok, err := filepath.Match(strings.ToLower(glob), strings.ToLower(base))
	return err == nil && ok
}

func atOrUnderExact(path, root string) bool {
	if path == root {
		return true
	}
	if root == "/" {
		return strings.HasPrefix(path, "/")
	}
	return strings.HasPrefix(path, root+"/")
}

// normalizeForCompare cleans paths for identity checks.
func normalizeForCompare(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." {
		return ""
	}
	if clean == "/" {
		return clean
	}
	return strings.TrimSuffix(clean, "/")
}

// nearestExistingDir returns the deepest existing ancestor.
func nearestExistingDir(path string) string {
	p := filepath.Clean(strings.TrimSpace(path))
	if p == "" || p == "." {
		return ""
	}
	for {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}
