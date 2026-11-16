package survey

import (
	"context"
)

type stubBranchWorkspace struct{}

func (stubBranchWorkspace) ValidateMeta(context.Context) error          { return nil }
func (stubBranchWorkspace) EnsureParents(context.Context, string) error { return nil }
