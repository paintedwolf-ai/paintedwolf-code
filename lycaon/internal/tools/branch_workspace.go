package tools

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// BranchWorkspace validates and prepares a complete worker branch.
type BranchWorkspace interface {
	ValidateMeta(ctx context.Context) error
	EnsureParents(ctx context.Context, branchRel string) error
}

// RequireBranchWorkspace validates worker-branch context wiring.
func RequireBranchWorkspace(tctx ToolContext) error {
	if strings.TrimSpace(tctx.Source.WorkerBranchRoot) == "" {
		return nil
	}
	if tctx.Source.BranchWorkspace == nil {
		return fmt.Errorf("worker branch workspace not configured: %w", fs.ErrInvalid)
	}
	return nil
}

// ValidateWorkerBranch checks branch metadata before process execution.
func ValidateWorkerBranch(ctx context.Context, tctx ToolContext) error {
	if err := RequireBranchWorkspace(tctx); err != nil {
		return err
	}
	if strings.TrimSpace(tctx.Source.WorkerBranchRoot) == "" {
		return nil
	}
	return tctx.Source.BranchWorkspace.ValidateMeta(ctx)
}

// RequiresWorkerBranch reports whether the named tool must claim a private
// write-worker branch before Run (catalog requires_worker_branch).
func RequiresWorkerBranch(tool string) bool {
	return slices.Contains(workerBranchTools, strings.TrimSpace(tool))
}
