package repomap

import (
	"context"

	"github.com/lycaon/lycaon/internal/tools"
)

// UnionOrientationBrief builds a multi-root structural brief when path is union-scoped.
type UnionOrientationBrief func(ctx context.Context, tctx tools.ToolContext, subpath string) (string, error)
