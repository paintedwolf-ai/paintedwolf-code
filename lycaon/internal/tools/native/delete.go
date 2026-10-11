package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// DeleteTool removes files and empty directories within write scope.
type DeleteTool struct {
	Boundary *sandbox.Boundary
}

func (t *DeleteTool) Name() string { return "delete" }

type deleteResponse struct {
	Deleted []string `json:"deleted"`
}

func (t *DeleteTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	paths, err := parseDeletePaths(args)
	if err != nil {
		return "", err
	}
	prof := tctx.ProfileID()
	filesOnly, _ := args["files_only"].(bool)
	resp := deleteResponse{Deleted: make([]string, 0, len(paths))}
	defer func() {
		if runErr != nil && len(resp.Deleted) > 0 {
			output, runErr = interruptedMutationBatch(resp, runErr)
		}
	}()
	for _, relPath := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		relSlash := filepath.ToSlash(relPath)
		resolved, err := assertDeleteWritePath(ctx, t.Boundary, tctx, relPath, prof)
		if err != nil {
			return "", err
		}
		fullPath := resolved.Abs
		info, err := os.Lstat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return "", &toolrejection.ToolReject{
					Code: "DELETE_NOT_FOUND",
					Data: map[string]any{"path": relSlash},
				}
			}
			return "", fmt.Errorf("delete %s: %w", relPath, err)
		}
		if info.IsDir() {
			if filesOnly {
				return "", &toolrejection.ToolReject{Code: "DELETE_IS_DIRECTORY", Data: map[string]any{"path": relSlash}}
			}
			entries, readErr := os.ReadDir(fullPath)
			if readErr != nil {
				return "", fmt.Errorf("delete %s: %w", relPath, readErr)
			}
			if len(entries) > 0 {
				return "", &toolrejection.ToolReject{
					Code: "DELETE_NOT_EMPTY",
					Data: map[string]any{"path": relSlash, "entries": len(entries)},
				}
			}
		}
		if err := removeAgentPath(ctx, tctx, resolvedMutationTarget(resolved)); err != nil {
			return "", fmt.Errorf("delete %s: %w", relPath, err)
		}
		resp.Deleted = append(resp.Deleted, relSlash)
		afterSuccessfulMutation(ctx, tctx, relSlash)
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
