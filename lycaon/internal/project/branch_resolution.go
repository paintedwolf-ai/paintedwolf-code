package project

import (
	"context"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// ResolveBranch resolves a durable history identity against current roots.
func ResolveBranch(ctx context.Context, database db.Handle, p *Project, branch sourcebranch.ID) (*Project, error) {
	if p == nil {
		return nil, ErrNotFound
	}
	if p.SourceBranch == branch {
		return p, nil
	}
	id, ok := branch.WorktreeID()
	if !ok {
		return nil, ErrRootNotFound
	}
	var binding Binding
	err := database.QueryRowContext(ctx, `SELECT id,toplevel,worktree_path,branch,base_branch FROM source_worktrees WHERE project_id=? AND id=?`, p.ID, id).Scan(&binding.ID, &binding.Toplevel, &binding.WorktreePath, &binding.Branch, &binding.BaseBranch)
	if err != nil {
		return nil, err
	}
	if err := git.NewManager().ValidateWorktree(ctx, binding.Toplevel, binding.WorktreePath, binding.Branch); err != nil {
		return nil, err
	}
	return WithWorktree(p, binding), nil
}
