package call

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// NormalizeRelativePath returns a clean repo-relative path or empty when invalid.
func NormalizeRelativePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrPathEscape
	}
	rel := filepath.ToSlash(filepath.Clean(raw))
	if rel == "." || sandbox.HasParentTraversal(rel) || filepath.IsAbs(rel) {
		return "", ErrPathEscape
	}
	return rel, nil
}
