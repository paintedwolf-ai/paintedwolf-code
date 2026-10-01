package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitHistoryContext supplies invocation identity without teaching Git about tools.
func GitHistoryContext(ctx context.Context, tc ToolContext) (context.Context, error) {
	ledger, ok := tc.SourceLedger.(interface {
		GitMutationContext(context.Context, string, []sourceledger.RootSpec, sourceledger.Contributor) context.Context
	})
	if !ok {
		return ctx, nil
	}
	roots := make([]sourceledger.RootSpec, 0, len(tc.Roots))
	for _, root := range tc.Roots {
		branch, err := tc.SourceBranch(root.ID)
		if err != nil {
			return nil, err
		}
		roots = append(roots, sourceledger.RootSpec{ID: root.ID, Path: root.Path, BranchID: branch})
	}
	return ledger.GitMutationContext(ctx, tc.ProjectID, roots, sourceledger.Contributor{
		Origin: api.SourceChangeOriginAgent, SessionID: tc.SessionID, Turn: tc.UserTurn,
		ToolCallID: tc.ToolCallID, ToolName: tc.Invocation.ToolName, JobID: tc.WorkerJobID,
	}), nil
}
