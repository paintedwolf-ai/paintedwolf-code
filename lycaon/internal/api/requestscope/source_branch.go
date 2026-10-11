package requestscope

import (
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/workspace"
)

func SourceProjectInBranch(p *project.Project, branchRoot string) (*project.Project, error) {
	if branchRoot == "" {
		return p, nil
	}
	roots, err := workspace.BranchRootRefs(branchRoot)
	if err != nil {
		return nil, err
	}
	attached := make(map[string]bool, len(p.Roots))
	for _, root := range p.Roots {
		attached[root.ID] = true
	}
	scoped := *p
	scoped.Roots = nil
	for _, root := range roots {
		if attached[root.ID] {
			scoped.Roots = append(scoped.Roots, project.Root{ID: root.ID, Path: root.Path, Label: root.Label, IsPrimary: root.IsPrimary})
		}
	}
	return &scoped, nil
}
