package native

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// ChmodTool changes permission bits on repo-relative paths within write scope.
type ChmodTool struct {
	Boundary *sandbox.Boundary
}

func (t *ChmodTool) Name() string { return "chmod" }

type chmodResult struct {
	Path       string `json:"path"`
	ModeBefore string `json:"mode_before"`
	ModeAfter  string `json:"mode_after"`
}

type chmodResponse struct {
	Results []chmodResult `json:"results"`
}

func (t *ChmodTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	paths, err := parseChmodPaths(args)
	if err != nil {
		return "", err
	}
	modeSpec, _ := args["mode"].(string)
	if strings.TrimSpace(modeSpec) == "" {
		return "", toolkit.MissingArg("mode")
	}

	resp := chmodResponse{Results: make([]chmodResult, 0, len(paths))}
	defer func() {
		if runErr != nil && len(resp.Results) > 0 {
			output, runErr = interruptedMutationBatch(resp, runErr)
		}
	}()
	prof := tctx.ProfileID()
	for _, relPath := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		target, modeBefore, err := assertChmodTarget(ctx, t.Boundary, tctx, relPath, prof)
		if err != nil {
			return "", err
		}
		modeAfter, err := resolveChmodMode(modeBefore, modeSpec)
		if err != nil {
			return "", err
		}
		if err := chmodAgentPath(ctx, tctx, target, modeAfter); err != nil {
			return "", fmt.Errorf("chmod %s: %w", relPath, err)
		}
		relSlash := filepath.ToSlash(relPath)
		resp.Results = append(resp.Results, chmodResult{
			Path:       relSlash,
			ModeBefore: sourceview.FormatFileMode(uint32(modeBefore.Perm())),
			ModeAfter:  sourceview.FormatFileMode(uint32(modeAfter.Perm())),
		})
		afterSuccessfulMutation(ctx, tctx, relSlash)
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
