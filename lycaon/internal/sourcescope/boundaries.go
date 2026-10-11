package sourcescope

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	BoundaryCurated        = "curated"
	BoundaryNestedCheckout = "nested_checkout"
)

// BoundaryDir classifies eager discovery without changing path admission.
func (s *Scope) BoundaryDir(rel string) string {
	rel = cleanRel(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	segments := strings.Split(rel, "/")
	for _, pattern := range s.boundaries {
		parts := strings.Split(pattern, "/")
		if len(parts) > len(segments) {
			continue
		}
		candidate := strings.Join(segments[len(segments)-len(parts):], "/")
		if matches, _ := path.Match(pattern, candidate); matches {
			return BoundaryCurated
		}
	}
	if s.plane.NestedCheckouts {
		marker, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(rel), ".git"))
		if err == nil && (marker.IsDir() || marker.Mode().IsRegular()) {
			return BoundaryNestedCheckout
		}
	}
	return ""
}

// BoundaryPath locates the first lazy ancestor of an explicitly requested path.
func (s *Scope) BoundaryPath(rel string, isDir bool) string {
	rel = cleanRel(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	segments := strings.Split(rel, "/")
	if !isDir {
		segments = segments[:len(segments)-1]
	}
	for i := range segments {
		if reason := s.BoundaryDir(strings.Join(segments[:i+1], "/")); reason != "" {
			return reason
		}
	}
	return ""
}
