package native

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// MoveTool renames files within one held write-root capability.
type MoveTool struct {
	Boundary *sandbox.Boundary
}

type moveResult struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type moveResponse struct {
	Moved []moveResult `json:"moved"`
}

func (t *MoveTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pairs, err := parseMovePairs(args)
	if err != nil {
		return "", err
	}
	prof := tctx.ProfileID()
	resp := moveResponse{Moved: make([]moveResult, 0, len(pairs))}
	defer func() {
		if runErr != nil && len(resp.Moved) > 0 {
			output, runErr = interruptedMutationBatch(resp, runErr)
		}
	}()
	for _, pair := range pairs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fromSlash := filepath.ToSlash(pair.From)
		toSlash := filepath.ToSlash(pair.To)
		srcResolved, err := assertMutationFileSource(
			ctx, t.Boundary, tctx, pair.From, prof, "move",
			"MOVE_NOT_FOUND", "MOVE_IS_DIRECTORY", "MOVE_SYMLINK_ESCAPE",
			assertMoveWritePath,
		)
		if err != nil {
			return "", err
		}
		dstResolved, err := assertMoveWritePath(ctx, t.Boundary, tctx, pair.To, prof)
		if err != nil {
			return "", err
		}
		if srcResolved.External != dstResolved.External ||
			srcResolved.Root.ID != dstResolved.Root.ID ||
			filepath.Clean(srcResolved.Root.Path) != filepath.Clean(dstResolved.Root.Path) {
			return "", &tools.ToolReject{
				Code: "MOVE_CROSS_ROOT",
				Data: map[string]any{"from": fromSlash, "to": toSlash},
			}
		}
		if err := renameAgentPath(ctx, tctx, resolvedMutationTarget(srcResolved), resolvedMutationTarget(dstResolved)); err != nil {
			return "", fmt.Errorf("move %s -> %s: %w", pair.From, pair.To, err)
		}
		resp.Moved = append(resp.Moved, moveResult{
			From: fromSlash,
			To:   toSlash,
		})
		afterSuccessfulMutation(ctx, tctx, fromSlash, toSlash)
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
