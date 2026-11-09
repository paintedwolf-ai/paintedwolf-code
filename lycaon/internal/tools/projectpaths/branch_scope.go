package projectpaths

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"os"
	"path/filepath"
	"strings"
)

func resolveUnderBranch(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, branch, modelPath string, op sandbox.PathOp) (Resolved, error) {
	// Keep operand bytes; whitespace may name a real entry.
	if modelPath == "" {
		return Resolved{}, fmt.Errorf("path required")
	}
	if tctx.Source.BranchWorkspace == nil {
		return Resolved{}, fmt.Errorf("worker branch workspace not configured")
	}
	var branchRel, displayPath string
	if filepath.IsAbs(modelPath) {
		var err error
		branchRel, err = relativeUnderBranch(branch, modelPath)
		if err != nil {
			return Resolved{}, mapResolveErr(fmt.Errorf("%w: %q", projectroot.ErrPathEscape, modelPath), modelPath)
		}
		displayPath = workerBranchDisplayPath(tctx, branchRel)
	} else {
		var err error
		branchRel, displayPath, err = projectroot.WorkerBranchRelative(tctx.Source.Roots, tctx.Source.ActiveRootID, modelPath)
		if err != nil {
			return Resolved{}, rootResolveErr(tctx, err, modelPath)
		}
	}
	scopeRel := filepath.ToSlash(filepath.Clean(branchRel))
	if scopeRel == "." {
		scopeRel = ""
	}
	if settingsoverlay.IsRecoveryRelPath(scopeRel) || settingsoverlay.HasRecoverySegment(scopeRel) {
		return Resolved{}, mapResolveErr(fmt.Errorf("%w: %q", projectroot.ErrPathEscape, modelPath), modelPath)
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
		return Resolved{}, rootResolveErr(tctx, err, modelPath)
	}
	if op == sandbox.PathOpRead && settingsoverlay.IsRecoveryOnlyDir(abs) {
		return Resolved{}, &os.PathError{Op: "stat", Path: modelPath, Err: os.ErrNotExist}
	}
	var root projectroot.RootRef
	if len(tctx.Source.Roots) > 1 {
		if _, _, resolveErr := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, displayPath); resolveErr != nil {
			return Resolved{}, rootResolveErr(tctx, resolveErr, modelPath)
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
		}
	}
	display := displayPath
	if display == "" {
		display = scopeRel
		if primary, err := projectroot.PrimaryRoot(tctx.Source.Roots); err == nil {
			display = projectroot.Qualify(tctx.Source.Roots, primary.ID, root, abs)
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
	activeID := ""
	if active, err := projectroot.ActiveRoot(tctx.Source.Roots, tctx.Source.ActiveRootID); err == nil {
		activeID = active.ID
	} else if primary, err := projectroot.PrimaryRoot(tctx.Source.Roots); err == nil {
		activeID = primary.ID
	}
	branchDir, rest, _ := strings.Cut(rel, "/")
	for _, root := range tctx.Source.Roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		if err == nil && dir == branchDir {
			if root.ID == activeID {
				if rest == "" {
					return "."
				}
				return rest
			}
			if rest == "" {
				return "@" + root.Label
			}
			return "@" + root.Label + "/" + rest
		}
	}
	return rel
}

func relativeUnderBranch(branch, modelPath string) (string, error) {
	cleanBranch, cleanPath := filepath.Clean(branch), filepath.Clean(modelPath)
	if rel, err := filepath.Rel(cleanBranch, cleanPath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel, nil
	}
	canonBranch := filepath.FromSlash(fspath.CanonicalPath(cleanBranch))
	canonPath := filepath.FromSlash(fspath.CanonicalPath(cleanPath))
	if canonBranch != "" && canonPath != "" {
		if rel, err := filepath.Rel(canonBranch, canonPath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel, nil
		}
	}
	return "", projectroot.ErrPathEscape
}

func prepareBranchPath(ctx context.Context, tctx tools.ToolContext, scopeRel string, op sandbox.PathOp) error {
	branch := tctx.Source.BranchWorkspace
	if branch == nil {
		return fmt.Errorf("worker branch workspace not configured")
	}
	if err := branch.ValidateMeta(ctx); err != nil {
		return err
	}
	rel := scopeRel
	if rel == "" || rel == "." {
		return nil
	}
	if op == sandbox.PathOpWrite {
		return branch.EnsureParents(ctx, rel)
	}
	return nil
}

func commandCwdUnderBranch(ctx context.Context, tctx tools.ToolContext, branch, cwdArg string) (abs, display string, err error) {
	// Cwd is an exact operand, not display text.
	if cwdArg == "" {
		return branch, ".", nil
	}
	branchRel, displayPath := "", ""
	if filepath.IsAbs(cwdArg) {
		// An in-root absolute cwd maps into the worker branch; anything
		// outside the attached roots stays out of scope.
		mappedRel, mappedDisplay, mapErr := branchCwdFromAbs(tctx, cwdArg)
		if mapErr != nil {
			return "", "", &toolrejection.ToolReject{
				Code: "CWD_OUT_OF_SCOPE",
				Data: map[string]any{"cwd": cwdArg},
			}
		}
		branchRel, displayPath = mappedRel, mappedDisplay
	} else {
		if tctx.Source.BranchWorkspace == nil {
			return "", "", fmt.Errorf("worker branch workspace not configured")
		}
		rel, disp, mapErr := projectroot.WorkerBranchRelative(tctx.Source.Roots, tctx.Source.ActiveRootID, cwdArg)
		if mapErr != nil {
			return "", "", &toolrejection.ToolReject{
				Code: "CWD_OUT_OF_SCOPE",
				Data: map[string]any{"cwd": cwdArg},
			}
		}
		branchRel, displayPath = rel, disp
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

// branchCwdFromAbs maps an absolute cwd under an attached root into the worker
// branch layout, mirroring WorkerBranchRelative for an already-resolved path.
func branchCwdFromAbs(tctx tools.ToolContext, cwdArg string) (branchRel, display string, err error) {
	resolved, root, resolveErr := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, cwdArg)
	if resolveErr != nil {
		return "", "", resolveErr
	}
	display = projectroot.Qualify(tctx.Source.Roots, tctx.Source.ActiveRootID, root, resolved)
	scopeRel := projectroot.ScopeRel(root, resolved)
	if len(tctx.Source.Roots) == 1 {
		return scopeRel, display, nil
	}
	dir, dirErr := projectroot.BranchDirForID(root.ID)
	if dirErr != nil {
		return "", "", dirErr
	}
	return filepath.ToSlash(filepath.Join(dir, scopeRel)), display, nil
}
