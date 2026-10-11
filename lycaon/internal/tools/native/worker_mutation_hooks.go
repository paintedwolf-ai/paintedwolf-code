package native

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

func skipWorkerMutationHooks(tctx tools.ToolContext) bool {
	return strings.TrimSpace(tctx.Identity.WorkerJobID) == ""
}

func beforeWorkerMutation(ctx context.Context, tctx tools.ToolContext, relPath string) error {
	if skipWorkerMutationHooks(tctx) || tctx.Source.WorkerCoord == nil {
		return nil
	}
	return tctx.Source.WorkerCoord.BeforeWorkerWrite(ctx, tctx, relPath)
}

func afterWorkerMutation(ctx context.Context, tctx tools.ToolContext, relPath string) {
	if skipWorkerMutationHooks(tctx) || tctx.Source.WorkerCoord == nil {
		return
	}
	tctx.Source.WorkerCoord.AfterWorkerWrite(ctx, tctx, relPath)
}
