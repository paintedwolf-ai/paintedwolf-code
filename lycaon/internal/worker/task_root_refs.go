package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskRootRefs returns a worker's captured or attached roots.
func TaskRootRefs(ctx context.Context, task *api.WorkerTask, projects ProjectStore) []projectroot.RootRef {
	if task == nil {
		return nil
	}
	if branchRoot := strings.TrimSpace(task.WorkspaceRoot); branchRoot != "" {
		// The recorded topology outlives the tree, which retention may reclaim.
		roots, err := workspace.LoadBranchRoots(branchRoot)
		if err != nil {
			return nil
		}
		return roots
	}
	if projects != nil && strings.TrimSpace(task.ProjectID) != "" {
		if p, err := projects.Get(ctx, task.ProjectID); err == nil && p != nil {
			if refs := project.RootRefsFrom(p); len(refs) > 0 {
				return refs
			}
		}
	}
	path := strings.TrimSpace(task.WorkspacePath)
	rootID := strings.TrimSpace(task.WorkspaceRootID)
	if path == "" || rootID == "" {
		return nil
	}
	return []projectroot.RootRef{{
		ID:        rootID,
		Path:      path,
		IsPrimary: true,
	}}
}
