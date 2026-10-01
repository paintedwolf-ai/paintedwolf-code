package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// EnsureParents creates branch directories for a path.
func EnsureParents(branchRoot, rel string) error {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" || !filepath.IsAbs(branchRoot) {
		return fmt.Errorf("absolute branch root required")
	}
	branchRoot = filepath.Clean(branchRoot)
	rel = strings.TrimSpace(rel)
	if filepath.IsAbs(filepath.FromSlash(rel)) || sandbox.HasParentTraversal(rel) {
		return fmt.Errorf("invalid branch path %q", rel)
	}
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return nil
	}
	parent := filepath.Dir(filepath.FromSlash(rel))
	if parent == "." || parent == string(filepath.Separator) {
		return nil
	}
	return fseffect.MkdirAll(fseffect.Location{Root: branchRoot, Rel: parent}, 0o750)
}
