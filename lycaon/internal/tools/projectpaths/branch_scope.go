package projectpaths

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"os"
	"path/filepath"
	"strings"
)

func resolveUnderBranch(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, branch, modelPath string, op sandbox.PathOp) (Resolved, error) {
	modelPath = strings.TrimSpace(modelPath)
	if modelPath == "" {
		return Resolved{}, fmt.Errorf("path required")
	}
	if tctx.Source.BranchWorkspace == nil {
		return Resolved{}, fmt.Errorf("worker branch workspace not configured")
	}
	var branchRel, displayPath string
	if filepath.IsAbs(modelPath) {
		var err error
		branchRel, err = filepath.Rel(filepath.Clean(branch), filepath.Clean(modelPath))
		if err != nil || branchRel == ".." || strings.HasPrefix(branchRel, ".."+string(filepath.Separator)) {
			return Resolved{}, mapResolveErr(fmt.Errorf("%w: %q", projectroot.ErrPathEscape, modelPath), modelPath)
		}
		displayPath = workerBranchDisplayPath(tctx, branchRel)
	} else {
		var err error
		branchRel, displayPath, err = projectroot.WorkerBranchRelative(tctx.Source.Roots, tctx.Source.ActiveRootID, modelPath)
		if err != nil {
			return Resolved{}, mapResolveErr(err, modelPath)
		}
	}
	scopeRel := filepath.ToSlash(filepath.Clean(branchRel))
	if scopeRel == "." {
		scopeRel = ""
	}
	if err := prepareBranchPath(ctx, tctx, scopeRel, op); err != nil {
		return Resolved{}, err
	}
	var abs string
	var err error
	if b != nil {
		abs, err = b.ResolveAbs(branch, branchRel)
	} else {
		abs, _, err = projectroot.ResolveAbs([]projectroot.RootRef{{
			ID: "worker-branch", Path: branch, IsPrimary: true,
		}}, "worker-branch", branchRel)
	}
	if err != nil {
		return Resolved{}, mapResolveErr(err, modelPath)
	}
	var root projectroot.RootRef
	if len(tctx.Source.Roots) > 1 {
		if _, _, resolveErr := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, displayPath); resolveErr != nil {
			return Resolved{}, mapResolveErr(resolveErr, modelPath)
		}
	}
	root = projectroot.RootRef{ID: "worker-branch", Path: branch, IsPrimary: true}
	if b != nil {
		switch op {
		case sandbox.PathOpRead:
			if err := b.AssertReadScope(ctx, branch, scopeRel, tctx.ProfileID()); err != nil {
				return Resolved{}, err
			}
		case sandbox.PathOpWrite:
			if err := b.AssertPathAllowed(ctx, branch, scopeRel, sandbox.PathOpWrite); err != nil {
				return Resolved{}, err
			}
		default:
			if err := b.AssertPathAllowed(ctx, branch, scopeRel, op); err != nil {
				return Resolved{}, err
			}
		}
	}
	display := displayPath
	if display == "" {
		display = scopeRel
		if primary, err := projectroot.PrimaryRoot(tctx.Source.Roots); err == nil {
			display = projectroot.Qualify(primary, root, abs)
		}
	}
	return Resolved{
		Abs:         abs,
		DisplayPath: display,
		ScopeRel:    scopeRel,
		Root:        root,
	}, nil
}

func workerBranchDisplayPath(tctx tools.ToolContext, branchRel string) string {
	rel := filepath.ToSlash(filepath.Clean(branchRel))
	if len(tctx.Source.Roots) <= 1 {
		return rel
	}
	branchDir, rest, _ := strings.Cut(rel, "/")
	for _, root := range tctx.Source.Roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		if err == nil && dir == branchDir {
			if rest == "" {
				return "@" + root.Label
			}
			return "@" + root.Label + "/" + rest
		}
	}
	return rel
}

func prepareBranchPath(ctx context.Context, tctx tools.ToolContext, scopeRel string, op sandbox.PathOp) error {
	branch := tctx.Source.BranchWorkspace
	if branch == nil {
		return fmt.Errorf("worker branch workspace not configured")
	}
	if err := branch.ValidateMeta(ctx); err != nil {
		return err
	}
	rel := strings.TrimSpace(scopeRel)
	if rel == "" || rel == "." {
		return nil
	}
	if op == sandbox.PathOpWrite {
		return branch.EnsureParents(ctx, rel)
	}
	return nil
}

func commandCwdUnderBranch(ctx context.Context, tctx tools.ToolContext, branch, cwdArg string) (abs, display string, err error) {
	cwdArg = strings.TrimSpace(cwdArg)
	if cwdArg == "" {
		return branch, ".", nil
	}
	if filepath.IsAbs(cwdArg) {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	if tctx.Source.BranchWorkspace == nil {
		return "", "", fmt.Errorf("worker branch workspace not configured")
	}
	branchRel, displayPath, mapErr := projectroot.WorkerBranchRelative(tctx.Source.Roots, tctx.Source.ActiveRootID, cwdArg)
	if mapErr != nil {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	scopeRel := filepath.ToSlash(filepath.Clean(branchRel))
	if scopeRel == ".." || strings.HasPrefix(scopeRel, "../") {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	if scopeRel == "." {
		scopeRel = ""
	}
	if branchErr := prepareBranchPath(ctx, tctx, scopeRel, sandbox.PathOpRead); branchErr != nil {
		if os.IsNotExist(branchErr) || errors.Is(branchErr, os.ErrNotExist) {
			return "", "", &toolrejection.ToolReject{
				Code: "CWD_NOT_DIRECTORY",
				Data: map[string]any{"cwd": cwdArg},
			}
		}
		return "", "", branchErr
	}
	abs = filepath.Join(branch, filepath.FromSlash(scopeRel))
	rel, relErr := filepath.Rel(branch, abs)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	info, statErr := os.Stat(abs)
	if statErr != nil || !info.IsDir() {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_NOT_DIRECTORY",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	display = displayPath
	if scopeRel == "" {
		display = "."
	} else if display == "" {
		display = scopeRel
	}
	return abs, display, nil
}
