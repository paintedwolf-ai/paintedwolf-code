package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
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

// MoveInput is one complete move operation. NoClobber leaves an existing
// destination in place and skips that pair. Directories admits directory
// sources, renamed whole within one root onto an absent destination; the
// rename is journaled like a file rename, so Files history can undo it.
type MoveInput struct {
	Pairs       []FilePair
	NoClobber   bool
	Directories bool
}

// MoveResult lists completed and skipped pairs in order.
type MoveResult struct {
	Moved   []FilePair
	Skipped []FilePair
}

func (t *MoveTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pairs, err := parseMovePairs(args)
	if err != nil {
		return "", err
	}
	result, err := t.Move(ctx, MoveInput{Pairs: pairs}, tctx)
	resp := moveResponse{Moved: make([]moveResult, 0, len(result.Moved))}
	for _, pair := range result.Moved {
		resp.Moved = append(resp.Moved, moveResult(pair))
	}
	if err != nil {
		if len(resp.Moved) > 0 {
			return interruptedMutationBatch(resp, err)
		}
		return "", err
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Move renames each pair within one root under scope, review and recovery.
func (t *MoveTool) Move(ctx context.Context, in MoveInput, tctx tools.ToolContext) (MoveResult, error) {
	var result MoveResult
	prof := tctx.ProfileID()
	for _, pair := range in.Pairs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		fromSlash, toSlash := filepath.ToSlash(pair.From), filepath.ToSlash(pair.To)
		srcResolved, err := t.moveSource(ctx, tctx, pair.From, in.Directories)
		if err != nil {
			return result, err
		}
		dstResolved, err := assertMoveWritePath(ctx, t.Boundary, tctx, pair.To, prof)
		if err != nil {
			return result, err
		}
		if srcResolved.External != dstResolved.External || srcResolved.Root.ID != dstResolved.Root.ID ||
			filepath.Clean(srcResolved.Root.Path) != filepath.Clean(dstResolved.Root.Path) {
			return result, &toolrejection.ToolReject{Code: "MOVE_CROSS_ROOT", Data: map[string]any{"from": fromSlash, "to": toSlash}}
		}
		if in.NoClobber {
			if _, err := os.Lstat(dstResolved.Abs); err == nil {
				result.Skipped = append(result.Skipped, FilePair{From: fromSlash, To: toSlash})
				continue
			}
		}
		if err := renameAgentPath(ctx, tctx, resolvedMutationTarget(srcResolved), resolvedMutationTarget(dstResolved)); err != nil {
			return result, fmt.Errorf("move %s -> %s: %w", pair.From, pair.To, err)
		}
		result.Moved = append(result.Moved, FilePair{From: fromSlash, To: toSlash})
		afterSuccessfulMutation(ctx, tctx, fromSlash, toSlash)
	}
	return result, nil
}

// moveSource resolves a file source, or with directories a directory entry
// itself; a link to a directory stays a file-style entry.
func (t *MoveTool) moveSource(ctx context.Context, tctx tools.ToolContext, from string, directories bool) (projectpaths.Resolved, error) {
	prof := tctx.ProfileID()
	if directories {
		resolved, err := assertMoveWritePath(ctx, t.Boundary, tctx, from, prof)
		if err != nil {
			return projectpaths.Resolved{}, err
		}
		if info, err := os.Lstat(resolved.Abs); err == nil && info.IsDir() {
			return resolved, nil
		}
	}
	return assertMutationFileSource(ctx, t.Boundary, tctx, from, prof, "move",
		"MOVE_NOT_FOUND", "MOVE_IS_DIRECTORY", "MOVE_SYMLINK_ESCAPE", assertMoveWritePath)
}
