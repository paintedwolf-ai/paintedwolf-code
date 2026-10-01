package project

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
)

// Binding is one session's worktree over one repository, as the path resolver
// needs to see it. It is built from the durable row; this package does not read it.
type Binding struct {
	ID           string
	Toplevel     string // the repository's canonical toplevel
	WorktreePath string // the session's checkout
	Branch       string
	BaseBranch   string
}

// SubstituteWorktreeRoots relocates roots beneath the repository while preserving their identities.
func SubstituteWorktreeRoots(roots []projectroot.RootRef, b Binding) []projectroot.RootRef {
	if strings.TrimSpace(b.Toplevel) == "" || strings.TrimSpace(b.WorktreePath) == "" {
		return roots
	}
	topAbs, err := filepath.Abs(b.Toplevel)
	if err != nil {
		return roots
	}
	topAbs = filepath.Clean(topAbs)

	out := make([]projectroot.RootRef, len(roots))
	for i, r := range roots {
		out[i] = r
		rootAbs, err := filepath.Abs(r.Path)
		if err != nil {
			continue
		}
		rootAbs = filepath.Clean(rootAbs)
		rel, err := filepath.Rel(topAbs, rootAbs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		out[i].Path = filepath.Join(b.WorktreePath, rel)
	}
	return out
}
