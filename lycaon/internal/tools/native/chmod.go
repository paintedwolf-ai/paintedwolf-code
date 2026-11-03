package native

import (
	"errors"
	"time"

	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/toolrejection"
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

// TimesTool sets access and modification times within write scope.
type TimesTool struct {
	Boundary *sandbox.Boundary
}

// SetTimes applies the times to each existing path under scope, review and
// the source journal; a nil time is left unchanged. It returns the paths
// already changed when one fails. Unsupported platforms refuse honestly.
func (t *TimesTool) SetTimes(ctx context.Context, paths []string, access, modify *time.Time, tctx tools.ToolContext) ([]string, error) {
	var changed []string
	prof := tctx.ProfileID()
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		resolved, err := assertWritePath(ctx, t.Boundary, tctx, path, prof, "write", func(p string) error {
			return &toolrejection.ToolReject{Code: "WRITE_PATH_DENIED", Data: map[string]any{"path": p}}
		})
		if err != nil {
			return changed, err
		}
		if err := timesAgentPath(ctx, tctx, resolvedMutationTarget(resolved), access, modify); err != nil {
			if errors.Is(err, fseffect.ErrUnsupported) {
				return changed, &toolrejection.ToolReject{Code: "COMMAND_PLATFORM_UNAVAILABLE", Data: map[string]any{"path": filepath.ToSlash(path), "capability": "timestamps"}}
			}
			return changed, fmt.Errorf("set times %s: %w", path, err)
		}
		rel := filepath.ToSlash(resolved.ScopeRel)
		changed = append(changed, rel)
		afterSuccessfulMutation(ctx, tctx, rel)
	}
	return changed, nil
}
