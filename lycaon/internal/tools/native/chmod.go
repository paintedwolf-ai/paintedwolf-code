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

	results, err := t.Chmod(ctx, paths, modeSpec, tctx)
	resp := chmodResponse{Results: results}
	if resp.Results == nil {
		resp.Results = []chmodResult{}
	}
	if err != nil {
		if len(resp.Results) > 0 {
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

// Chmod applies one mode specification to each path under scope, review and
// the source journal, returning the changes already applied when one fails.
func (t *ChmodTool) Chmod(ctx context.Context, paths []string, modeSpec string, tctx tools.ToolContext) ([]chmodResult, error) {
	var results []chmodResult
	prof := tctx.ProfileID()
	for _, relPath := range paths {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		target, modeBefore, err := assertChmodTarget(ctx, t.Boundary, tctx, relPath, prof)
		if err != nil {
			return results, err
		}
		modeAfter, err := resolveChmodMode(modeBefore, modeSpec)
		if err != nil {
			return results, err
		}
		if err := chmodAgentPath(ctx, tctx, target, modeAfter); err != nil {
			return results, fmt.Errorf("chmod %s: %w", relPath, err)
		}
		relSlash := filepath.ToSlash(relPath)
		results = append(results, chmodResult{Path: relSlash, ModeBefore: sourceview.FormatFileMode(uint32(modeBefore.Perm())), ModeAfter: sourceview.FormatFileMode(uint32(modeAfter.Perm()))})
		afterSuccessfulMutation(ctx, tctx, relSlash)
	}
	return results, nil
}
