package native

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// assertProfileWriteScope applies profile restrictions to content writes, including redirects.
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
	if profile == coordinatorProfileID && tctx.TurnSurfaceID == toolcontract.SurfaceImplementInvestigate {
		if progress.IsShadowBoardPath(path) {
			return &toolrejection.ToolReject{
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
			toolcontract.CoordinatorProductWriteScope,
		)
	}
	return boundary.AssertWriteScope(
		ctx,
		projectDir,
		scopeRel,
		profile,
	)
}

// Worker write scopes are relative to the isolated branch root.
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
