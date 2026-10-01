package native

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

func skipWorkerMutationHooks(tctx tools.ToolContext) bool {
	return strings.TrimSpace(tctx.WorkerJobID) == ""
}

func beforeWorkerMutation(ctx context.Context, tctx tools.ToolContext, relPath string) error {
	if skipWorkerMutationHooks(tctx) || tctx.WorkerCoord == nil {
		return nil
	}
	return tctx.WorkerCoord.BeforeWorkerWrite(ctx, tctx, relPath)
}

func afterWorkerMutation(ctx context.Context, tctx tools.ToolContext, relPath string) {
	if skipWorkerMutationHooks(tctx) || tctx.WorkerCoord == nil {
		return
	}
	tctx.WorkerCoord.AfterWorkerWrite(ctx, tctx, relPath)
}
