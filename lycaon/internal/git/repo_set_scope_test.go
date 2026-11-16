package git_test

import (
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
)

func scopeRepos(roots []projectroot.RootRef) []git.RepoRef {
	out := make([]git.RepoRef, 0, len(roots))
	for _, root := range roots {
		out = append(out, git.RepoRef{ID: root.Path, Toplevel: root.Path, Label: root.Label, RootIDs: []string{root.ID}, Available: true})
	}
	return out
}
