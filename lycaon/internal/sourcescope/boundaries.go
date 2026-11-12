package sourcescope

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
)

const (
	BoundaryCurated        = "curated"
	BoundaryNestedCheckout = "nested_checkout"
	// BoundaryEditorOnly marks a root agents do not use.
	BoundaryEditorOnly = "editor_only"
	// BoundaryControlPlane marks the engine's own state, which no recursive
	// disclosure crosses.
	BoundaryControlPlane = "control_plane"
)

// observationFacts reads the host facts that bound observation of root.
func observationFacts(root string) (editorOnly bool, controlPlane *confine.ControlPlane) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return false, nil
	}
	editorOnly, _ = confine.AttachedWriteRootRefused(root)
	layout := confine.CurrentControlPlane()
	if !layout.Meets(fspath.CanonicalPath(root)) {
		return editorOnly, nil
	}
	return editorOnly, &layout
}

// ObservedOnDemand reports whether root is an editor-only root, which is never
// walked eagerly or watched.
func ObservedOnDemand(root string) bool {
	editorOnly, _ := observationFacts(root)
	return editorOnly
}

// InControlPlane reports whether rel is in the engine's own state.
func (s *Scope) InControlPlane(rel string) bool {
	rel = cleanRel(rel)
	if s.controlPlane == nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	return s.controlPlane.Contains(fspath.CanonicalPath(filepath.Join(s.root, filepath.FromSlash(rel))))
}

// BoundaryDir classifies eager discovery without changing path admission. An
// editor-only root is a boundary at the root, and every directory in the
// control plane is one.
func (s *Scope) BoundaryDir(rel string) string {
	rel = cleanRel(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	if rel == "." {
		if s.editorOnly {
			return BoundaryEditorOnly
		}
		return ""
	}
	if s.InControlPlane(rel) {
		return BoundaryControlPlane
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
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	if s.editorOnly {
		return BoundaryEditorOnly
	}
	if rel == "." {
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
