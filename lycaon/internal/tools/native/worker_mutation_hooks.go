package native

import (
	"context"
	"os"
	"strings"
	"syscall"

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

func observeCommittedWorkerWrite(ctx context.Context, tctx tools.ToolContext, absPath, sha256Hex string, size int64) {
	if skipWorkerMutationHooks(tctx) || tctx.Source.WorkerCoord == nil {
		return
	}
	coord, ok := tctx.Source.WorkerCoord.(tools.WorkerObservationCoordinator)
	if !ok || coord == nil {
		return
	}
	rootID, rel := "primary", ""
	if root, locRel, located := tctx.SourceLocation(absPath); located {
		rootID, rel = root.ID, locRel
	}
	var modNS int64
	var ino, dev uint64
	if info, err := os.Lstat(absPath); err == nil {
		modNS = info.ModTime().UnixNano()
		if size == 0 {
			size = info.Size()
		}
		if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil {
			ino, dev = uint64(sys.Ino), uint64(sys.Dev)
		}
	}
	coord.ObserveWorkerWrite(ctx, tctx, tools.WorkerFileObservationFacts{
		RootID: rootID, RelPath: rel, AbsPath: absPath,
		SHA256: sha256Hex, Size: size, ModTimeNS: modNS,
		Ino: ino, Dev: dev, Layer: "private", Complete: sha256Hex != "",
	})
}
