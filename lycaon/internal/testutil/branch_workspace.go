package testutil

import "context"

// CompleteBranchWorkspace accepts branch metadata operations.
type CompleteBranchWorkspace struct{}

func (CompleteBranchWorkspace) ValidateMeta(context.Context) error          { return nil }
func (CompleteBranchWorkspace) EnsureParents(context.Context, string) error { return nil }
