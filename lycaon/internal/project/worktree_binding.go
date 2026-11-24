package project

import (
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// Binding is one checkout projection over one repository, as the path resolver
// needs to see it.
type Binding struct {
	ID           string
	Toplevel     string // the repository's canonical toplevel
	WorktreePath string // the target checkout
	Branch       string
	BaseBranch   string
}

// SubstituteWorktreeRoots relocates roots beneath the repository while preserving their identities.
func SubstituteWorktreeRoots(roots []projectroot.RootRef, b Binding) []projectroot.RootRef {
	if strings.TrimSpace(b.Toplevel) == "" || strings.TrimSpace(b.WorktreePath) == "" {
		return roots
	}
	out := make([]projectroot.RootRef, len(roots))
	for i, r := range roots {
		out[i] = r
		if r.Outside || strings.TrimSpace(r.Path) == "" {
			continue
		}
		projected, ok := gitrepo.ProjectSubtree(b.Toplevel, b.WorktreePath, r.Path)
		if !ok {
			continue
		}
		out[i].Path = projected
	}
	return out
}
