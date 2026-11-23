package project

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// ResolveBranch resolves a durable history identity against current roots and live checkout continuity.
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
	checkout, err := worktrees.VerifyLiveCheckout(ctx, database, p.ID, id)
	if err != nil {
		return nil, err
	}
	return WithWorktree(p, Binding{
		ID:           checkout.ID,
		Toplevel:     checkout.Toplevel,
		WorktreePath: checkout.WorktreePath,
		Branch:       checkout.Branch,
		BaseBranch:   checkout.BaseBranch,
	}), nil
}
