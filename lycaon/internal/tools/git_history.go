package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitHistoryContext supplies invocation identity without teaching Git about tools.
func GitHistoryContext(ctx context.Context, tc ToolContext) (context.Context, error) {
	ledger := tc.Source.GitMutations
	if ledger == nil {
		return ctx, nil
	}
	writeRoot := strings.TrimSpace(HostWriteRoot(tc))
	privWorktree, finish, managed, _ := privategit.PrepareViewGitOperation(ctx, writeRoot, tc.Identity.WorkerJobID)
	if finish != nil {
		_ = finish()
	}

	roots := make([]sourceledger.RootSpec, 0, len(tc.Source.Roots)+2)
	for _, root := range tc.Source.Roots {
		branch, err := tc.SourceBranch(root.ID)
		if err != nil {
			return nil, err
		}
		roots = append(roots, sourceledger.RootSpec{ID: root.ID, Path: root.Path, BranchID: branch})
		if root.ID == tc.Source.ActiveRootID || root.IsPrimary {
			if writeRoot != "" && writeRoot != root.Path {
				roots = append(roots, sourceledger.RootSpec{ID: root.ID, Path: writeRoot, BranchID: branch})
			}
			if managed && privWorktree != "" && privWorktree != root.Path && privWorktree != writeRoot {
				roots = append(roots, sourceledger.RootSpec{ID: root.ID, Path: privWorktree, BranchID: branch})
			}
		}
	}
	return ledger.GitMutationContext(ctx, tc.Identity.ProjectID, roots, sourceledger.Contributor{
		Origin: api.SourceChangeOriginAgent, SessionID: tc.Identity.SessionID, Turn: tc.Identity.UserTurn,
		ToolCallID: tc.Identity.ToolCallID, ToolName: tc.Invocation.ToolName, JobID: tc.Identity.WorkerJobID,
	}), nil
}
