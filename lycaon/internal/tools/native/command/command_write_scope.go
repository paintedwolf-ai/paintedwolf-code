package command

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

const coordinatorProfileID = "coordinator"

func assertProfileWriteScope(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, path, tool string) error {
	if boundary == nil {
		return nil
	}
	resolved, err := projectpaths.ResolveWrite(ctx, nil, tctx, path)
	if err != nil {
		return err
	}
	return assertResolvedProfileWriteScope(ctx, boundary, tctx, resolved, path, tool)
}

func assertResolvedProfileWriteScope(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, resolved projectpaths.Resolved, path, tool string) error {
	if boundary == nil {
		return nil
	}
	profile := tctx.ProfileID()
	projectDir, scopeRel := workerWriteScopeDir(tctx, resolved)
	if profile == coordinatorProfileID && tctx.TurnSurfaceID == tools.SurfaceImplementInvestigate {
		if progress.IsShadowBoardPath(path) {
			return &tools.ToolReject{
				Code: "COORDINATOR_PROGRESS_SHADOW_BOARD",
				Data: map[string]any{
					"path": path,
					"tool": tool,
				},
			}
		}
		if err := boundary.AssertPathAllowed(ctx, projectDir, scopeRel, sandbox.PathOpWrite); err != nil {
			return err
		}
		return boundary.AssertNamedWriteScope(
			ctx,
			projectDir,
			scopeRel,
			tools.CoordinatorProductWriteScope,
		)
	}
	return boundary.AssertWriteScope(
		ctx,
		projectDir,
		scopeRel,
		profile,
	)
}

func workerWriteScopeDir(tctx tools.ToolContext, resolved projectpaths.Resolved) (projectDir, scopeRel string) {
	if branch := strings.TrimSpace(tctx.WorkerBranchRoot); branch != "" {
		projectDir = branch
		if canon, err := filepath.EvalSymlinks(branch); err == nil {
			projectDir = canon
		}
		scopeRel = resolved.ScopeRel
		return projectDir, scopeRel
	}
	return resolved.Root.Path, resolved.ScopeRel
}

func redirectWriteScopeReject(ctx context.Context, boundary *sandbox.Boundary, path, profileID, tool string, err error) error {
	if err == nil {
		return nil
	}
	var scopeErr *sandbox.ScopeError
	if !errors.As(err, &scopeErr) || scopeErr.Kind != sandbox.ScopeWrite {
		return err
	}
	if path == "" {
		path = scopeErr.Path
	}
	var globs []string
	if boundary != nil {
		globs = boundary.WriteGlobsForProfile(ctx, profileID)
	}
	if profileID == "" {
		profileID = tools.DefaultToolProfileID
	}
	return &tools.ToolReject{
		Code: "WRITE_SCOPE_DENIED",
		Data: map[string]any{
			"path":           path,
			"tool":           tool,
			"profile":        profileID,
			"patterns_list":  formatGlobsMarkdown(globs),
			"patterns_count": len(globs),
			"kind":           "redirect",
		},
	}
}

func formatGlobsMarkdown(globs []string) string {
	var b strings.Builder
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		fmt.Fprintf(&b, "- `%s`\n", g)
	}
	return strings.TrimRight(b.String(), "\n")
}
