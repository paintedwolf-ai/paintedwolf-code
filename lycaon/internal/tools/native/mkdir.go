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
	created, err := t.Mkdir(ctx, MkdirInput{Paths: paths, Mode: mode}, tctx)
	resp := mkdirResponse{Created: created}
	if resp.Created == nil {
		resp.Created = []string{}
	}
	if err != nil {
		if len(resp.Created) > 0 {
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

// Mkdir creates each directory under scope, review and the source journal.
// It returns the directories it created, in order, also when a later one fails.
func (t *MkdirTool) Mkdir(ctx context.Context, in MkdirInput, tctx tools.ToolContext) ([]string, error) {
	var created []string
	prof := tctx.ProfileID()
	for _, relPath := range in.Paths {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		relSlash := filepath.ToSlash(relPath)
		resolved, err := assertMkdirWritePath(ctx, t.Boundary, tctx, relPath, prof)
		if err != nil {
			return created, err
		}
		info, err := os.Lstat(resolved.Abs)
		if err == nil {
			if !info.IsDir() {
				return created, &toolrejection.ToolReject{Code: "MKDIR_FILE_EXISTS", Data: map[string]any{"path": relSlash}}
			}
		} else if !os.IsNotExist(err) {
			return created, fmt.Errorf("mkdir %s: %w", relPath, err)
		}
		if err := mkdirAgentPath(ctx, tctx, resolvedMutationTarget(resolved), in.Mode); err != nil {
			return created, fmt.Errorf("mkdir %s: %w", relPath, err)
		}
		if info == nil {
			created = append(created, relSlash)
		}
		afterSuccessfulMutation(ctx, tctx, relSlash)
	}
	return created, nil
}
