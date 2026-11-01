package native

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func skipWorkerMutationHooks(tctx tools.ToolContext) bool {
	return strings.TrimSpace(tctx.Identity.WorkerJobID) == ""
}

func beforeWorkerMutation(ctx context.Context, tctx tools.ToolContext, relPath string) error {
	if skipWorkerMutationHooks(tctx) || tctx.Source.WorkerCoord == nil {
		return nil
	}
	// Native commands address physical paths; coordination reserves logical paths.
	if filepath.IsAbs(relPath) {
		resolved, err := projectpaths.ResolveWrite(ctx, nil, tctx, relPath)
		if err != nil {
			return err
		}
		relPath = resolved.DisplayPath
	}
	return tctx.Source.WorkerCoord.BeforeWorkerWrite(ctx, tctx, relPath)
}

func afterWorkerMutation(ctx context.Context, tctx tools.ToolContext, relPath string) {
	if skipWorkerMutationHooks(tctx) || tctx.Source.WorkerCoord == nil {
		return
	}
	tctx.Source.WorkerCoord.AfterWorkerWrite(ctx, tctx, relPath)
}
