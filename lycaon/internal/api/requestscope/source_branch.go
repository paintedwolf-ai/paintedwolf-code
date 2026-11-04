package requestscope

import (
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/workspace"
)

// ResolveBranchConsumerEngine returns the active layered consumer.Engine registered for branchRoot, if any (#1664).
func ResolveBranchConsumerEngine(branchRoot string) (*consumer.Engine, bool) {
	return consumer.LookupBranchEngine(branchRoot)
}

func SourceProjectInBranch(p *project.Project, branchRoot string) (*project.Project, error) {
	if branchRoot == "" || p == nil {
		return p, nil
	}
	attached := make(map[string]bool, len(p.Roots))
	for _, root := range p.Roots {
		attached[root.ID] = true
	}
	if eng, ok := consumer.LookupBranchEngine(branchRoot); ok && eng != nil {
		specs := eng.MountedRootSpecs()
		if len(specs) > 0 {
			scoped := *p
			scoped.Roots = nil
			for _, spec := range specs {
				if attached[spec.ID] && spec.TrunkPath != "" {
					scoped.Roots = append(scoped.Roots, project.Root{
						ID:        spec.ID,
						Path:      spec.TrunkPath,
						Label:     spec.Label,
						IsPrimary: spec.IsPrimary,
					})
				}
			}
			if len(scoped.Roots) > 0 {
				return &scoped, nil
			}
		}
	}
	roots, err := workspace.BranchRootRefs(branchRoot)
	if err != nil {
		return nil, err
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
