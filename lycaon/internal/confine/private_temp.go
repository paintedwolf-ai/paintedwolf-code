package confine

import (
	"path/filepath"
	"strings"
)

// PrivateTemp identifies the sandbox directories that present session scratch.
// An empty ScratchRoot gives each launch its own filesystem.
type PrivateTemp struct {
	Paths       []string
	ScratchRoot string
}

// Resolve maps a sandbox operand to its backing path for host file operations.
func (t PrivateTemp) Resolve(path string) (string, bool) {
	if t.ScratchRoot == "" || !filepath.IsAbs(path) {
		return "", false
	}
	for _, alias := range t.Paths {
		rel, err := filepath.Rel(alias, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.Join(t.ScratchRoot, rel), true
		}
	}
	return "", false
}
