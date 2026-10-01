package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// MkdirTool creates directories within write scope.
type MkdirTool struct {
	Boundary *sandbox.Boundary
}

type mkdirResponse struct {
	Created []string `json:"created"`
}

func (t *MkdirTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	paths, err := parseMkdirPaths(args)
	if err != nil {
		return "", err
	}
	modeSpec, _ := args["mode"].(string)
	if strings.TrimSpace(modeSpec) == "" {
		modeSpec = "755"
	}
	mode, err := parseOctalChmodMode(modeSpec)
	if err != nil {
		reject := &tools.ToolReject{}
		if errors.As(err, &reject) {
			reject.Data["chmod_allowed_modes"] = allowedChmodModes()
			return "", &tools.ToolReject{
				Code: "MKDIR_MODE_DENIED",
				Data: reject.Data,
			}
		}
		return "", err
	}
	prof := tctx.ProfileID()
	resp := mkdirResponse{Created: make([]string, 0, len(paths))}
	defer func() {
		if runErr != nil && len(resp.Created) > 0 {
			output, runErr = interruptedMutationBatch(resp, runErr)
		}
	}()
	for _, relPath := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		relSlash := filepath.ToSlash(relPath)
		resolved, err := assertMkdirWritePath(ctx, t.Boundary, tctx, relPath, prof)
		if err != nil {
			return "", err
		}
		fullPath := resolved.Abs
		info, err := os.Lstat(fullPath)
		if err == nil {
			if !info.IsDir() {
				return "", &tools.ToolReject{
					Code: "MKDIR_FILE_EXISTS",
					Data: map[string]any{"path": relSlash},
				}
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("mkdir %s: %w", relPath, err)
		}
		if err := mkdirAgentPath(ctx, tctx, resolvedMutationTarget(resolved), mode); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", relPath, err)
		}
		if info == nil {
			resp.Created = append(resp.Created, relSlash)
		}
		afterSuccessfulMutation(ctx, tctx, relSlash)
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
