package project

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
)

// WorkspaceID identifies the physical roots currently resolved for the project.
func (p *Project) WorkspaceID() string {
	if p == nil {
		return ""
	}
	return sourceworkspace.ID(p.ID, RootRefsFrom(p))
}

// ResolveWorkspaceRoot selects the requested or primary project root.
func ResolveWorkspaceRoot(p *Project, workspaceRootID string) (rootID, workspacePath string, err error) {
	if p == nil || len(p.Roots) == 0 {
		return "", "", nil
	}
	want := strings.TrimSpace(workspaceRootID)
	if want != "" {
		for _, root := range p.Roots {
			if root.ID == want {
				return root.ID, root.Path, nil
			}
		}
		return "", "", fmt.Errorf("%w: workspace_root_id %s", ErrNotFound, want)
	}
	for _, root := range p.Roots {
		if root.IsPrimary {
			return root.ID, root.Path, nil
		}
	}
	return "", "", ErrRootNotFound
}

// WithRootRefs resolves a physical workspace without changing logical root IDs.
func WithRootRefs(p *Project, roots []projectroot.RootRef) *Project {
	if p == nil {
		return nil
	}
	out := *p
	out.Roots = append([]Root(nil), p.Roots...)
	paths := make(map[string]string, len(roots))
	for _, root := range roots {
		paths[root.ID] = root.Path
	}
	for i := range out.Roots {
		if path, ok := paths[out.Roots[i].ID]; ok {
			out.Roots[i].Path = path
		}
	}
	return &out
}

// WithWorktree applies one durable checkout binding to the current root set.
func WithWorktree(p *Project, binding Binding) *Project {
	out := WithRootRefs(p, SubstituteWorktreeRoots(RootRefsFrom(p), binding))
	out.SourceBranch = sourcebranch.ForWorktree(binding.ID)
	out.RootBranches = make(map[string]sourcebranch.ID, len(p.Roots))
	for i, root := range p.Roots {
		out.RootBranches[root.ID] = p.BranchForRoot(root.ID)
		if root.Path != out.Roots[i].Path {
			out.RootBranches[root.ID] = out.SourceBranch
		}
	}
	return out
}

// BranchForRoot preserves sharing for roots outside a bound worktree.
func (p *Project) BranchForRoot(rootID string) sourcebranch.ID {
	if branch, ok := p.RootBranches[rootID]; ok {
		return branch
	}
	return p.SourceBranch
}
