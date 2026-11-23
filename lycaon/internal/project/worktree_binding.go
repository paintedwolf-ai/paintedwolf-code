package project

import (
	"path/filepath"
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
// Roots inside the repository whose projected path crosses a nested repository boundary are omitted
// so reads and writes never fall back to the attached checkout.
func SubstituteWorktreeRoots(roots []projectroot.RootRef, b Binding) []projectroot.RootRef {
	if strings.TrimSpace(b.Toplevel) == "" || strings.TrimSpace(b.WorktreePath) == "" {
		return roots
	}
	out := make([]projectroot.RootRef, 0, len(roots))
	for _, r := range roots {
		if r.Outside || strings.TrimSpace(r.Path) == "" {
			out = append(out, r)
			continue
		}
		if !rootInsideToplevel(b.Toplevel, r.Path) {
			out = append(out, r)
			continue
		}
		projected, ok := gitrepo.ProjectSubtree(b.Toplevel, b.WorktreePath, r.Path)
		if !ok {
			continue
		}
		r.Path = projected
		out = append(out, r)
	}
	return out
}

func rootInsideToplevel(toplevel, rootPath string) bool {
	srcTop := gitrepo.CanonicalDir(toplevel)
	rootCanon := gitrepo.CanonicalDir(rootPath)
	if srcTop == "" || rootCanon == "" {
		return false
	}
	rel, err := filepath.Rel(srcTop, rootCanon)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
