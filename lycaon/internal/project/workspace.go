package project

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
)

// WorkspaceID identifies the physical roots currently resolved for the project.
func (p *Project) WorkspaceID() string {
	if p == nil {
		return ""
	}
	roots := p.SourceRoots()
	attached := roots[:0:0]
	for _, root := range roots {
		if !root.Outside {
			attached = append(attached, root)
		}
	}
	return sourceworkspace.ID(p.ID, attached)
}

// ResolveWorkspaceRoot selects the requested or primary admitted root for a
// chat. A project without admitted roots has no workspace root.
func ResolveWorkspaceRoot(p *Project, workspaceRootID string) (rootID, workspacePath string, err error) {
	roots := AdmittedRoots(p)
	if len(roots) == 0 {
		return "", "", nil
	}
	want := strings.TrimSpace(workspaceRootID)
	if want == "" {
		if !roots[0].IsPrimary {
			return "", "", ErrRootNotFound
		}
		return roots[0].ID, roots[0].Path, nil
	}
	for _, root := range roots {
		if root.ID == want {
			return root.ID, root.Path, nil
		}
	}
	return "", "", fmt.Errorf("%w: workspace_root_id %s", ErrNotFound, want)
}

// WithWorktree applies one durable checkout binding to the current root set.
func WithWorktree(p *Project, binding Binding) *Project {
	if p == nil {
		return nil
	}
	substituted := SubstituteWorktreeRoots(p.SourceRoots(), binding)
	paths := make(map[string]string, len(substituted))
	for _, root := range substituted {
		paths[root.ID] = root.Path
	}
	branch := sourcebranch.ForWorktree(binding.ID)
	if sameCheckoutRoot(binding.Toplevel, binding.WorktreePath) {
		branch = sourcebranch.Trunk
	}
	out := *p
	out.SourceBranch = branch
	out.Roots = make([]Root, 0, len(p.Roots))
	out.RootBranches = make(map[string]sourcebranch.ID, len(p.Roots))
	for _, root := range p.Roots {
		path, ok := paths[root.ID]
		if !ok {
			continue
		}
		copied := root
		copied.Path = path
		out.Roots = append(out.Roots, copied)
		out.RootBranches[root.ID] = p.BranchForRoot(root.ID)
		if rootInsideToplevel(binding.Toplevel, root.Path) {
			out.RootBranches[root.ID] = branch
		}
	}
	return &out
}

func sameCheckoutRoot(a, b string) bool {
	ca := gitrepo.CanonicalDir(a)
	cb := gitrepo.CanonicalDir(b)
	return ca != "" && ca == cb
}

// BranchForRoot preserves sharing for roots outside a bound worktree. The
// outside namespace has one history, on the trunk.
func (p *Project) BranchForRoot(rootID string) sourcebranch.ID {
	if rootID != "" && rootID == p.Outside.ID {
		return sourcebranch.Trunk
	}
	if branch, ok := p.RootBranches[rootID]; ok {
		return branch
	}
	return p.SourceBranch
}
