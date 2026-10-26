package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
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
		reject := &toolrejection.ToolReject{}
		if errors.As(err, &reject) {
			reject.Data["chmod_allowed_modes"] = allowedChmodModes()
			return "", &toolrejection.ToolReject{
				Code: "MKDIR_MODE_DENIED",
				Data: reject.Data,
			}
		}
		return "", err
	}
	result, err := t.Mkdir(ctx, MkdirInput{Paths: paths, Mode: mode}, tctx)
	resp := mkdirResponse{Created: result.Created}
	if resp.Created == nil {
		resp.Created = []string{}
	}
	if err != nil {
		if result.Effects > 0 {
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

// MkdirInput is one complete mkdir operation; missing parents are created.
type MkdirInput struct {
	Paths []string
	Mode  os.FileMode
}

// MkdirResult retains requested directories and actual creations, including partial errors.
type MkdirResult struct {
	Created []string
	Effects int
}

// Mkdir creates each directory under scope, review and the source journal.
// It returns the directories it created, in order, also when a later one fails.
func (t *MkdirTool) Mkdir(ctx context.Context, in MkdirInput, tctx tools.ToolContext) (MkdirResult, error) {
	var result MkdirResult
	prof := tctx.ProfileID()
	for _, relPath := range in.Paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		relSlash := filepath.ToSlash(relPath)
		resolved, err := assertMkdirWritePath(ctx, t.Boundary, tctx, relPath, prof)
		if err != nil {
			return result, err
		}
		info, err := os.Lstat(resolved.Abs)
		if err == nil {
			if !info.IsDir() {
				return result, &toolrejection.ToolReject{Code: "MKDIR_FILE_EXISTS", Data: map[string]any{"path": relSlash}}
			}
		} else if !os.IsNotExist(err) {
			return result, fmt.Errorf("mkdir %s: %w", relPath, err)
		}
		count, err := mkdirAgentPath(ctx, tctx, resolvedMutationTarget(resolved), in.Mode)
		result.Effects += count
		if err == nil && info == nil && count > 0 {
			result.Created = append(result.Created, relSlash)
		}
		if err != nil {
			return result, fmt.Errorf("mkdir %s: %w", relPath, err)
		}
		afterSuccessfulMutation(ctx, tctx, relSlash)
	}
	return result, nil
}
