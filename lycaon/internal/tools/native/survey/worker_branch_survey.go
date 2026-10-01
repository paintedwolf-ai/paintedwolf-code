package survey

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
)

func branchRelUnderWorker(branchRoot, abs string) (string, bool) {
	branchRoot = filepath.Clean(branchRoot)
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(branchRoot, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return rel, true
}

func workerBranchReadDir(
	ctx context.Context,
	tctx tools.ToolContext,
	fullPath string,
	opts sandbox.SurveyOptions,
) ([]sandbox.SurveyEntry, bool, error) {
	branch := strings.TrimSpace(tctx.WorkerBranchRoot)
	if branch == "" {
		return nil, false, nil
	}
	rel, ok := branchRelUnderWorker(branch, fullPath)
	if !ok {
		return nil, false, nil
	}
	if err := tools.RequireBranchWorkspace(tctx); err != nil {
		return nil, true, err
	}
	if err := tctx.BranchWorkspace.ValidateMeta(ctx); err != nil {
		return nil, true, err
	}
	entries, err := workspace.OverlaySurveyReadDir(branch, rel, opts)
	return entries, true, err
}

func workerBranchSurveyWalk(
	ctx context.Context,
	tctx tools.ToolContext,
	fullRoot string,
	opts sandbox.SurveyOptions,
	fn func(sandbox.SurveyEntry) (sandbox.SurveyAction, error),
) (bool, error) {
	branch := strings.TrimSpace(tctx.WorkerBranchRoot)
	if branch == "" {
		return false, nil
	}
	rel, ok := branchRelUnderWorker(branch, fullRoot)
	if !ok {
		return false, nil
	}
	if err := tools.RequireBranchWorkspace(tctx); err != nil {
		return true, err
	}
	if err := tctx.BranchWorkspace.ValidateMeta(ctx); err != nil {
		return true, err
	}
	branchWalk := branch
	if rel != "" {
		branchWalk = filepath.Join(branch, filepath.FromSlash(rel))
	}
	return true, workspace.OverlaySurveyWalk(ctx, branchWalk, opts, fn)
}

func ensureBranchFileForRead(ctx context.Context, tctx tools.ToolContext, abs string) error {
	if strings.TrimSpace(tctx.WorkerBranchRoot) == "" {
		return nil
	}
	if err := tools.RequireBranchWorkspace(tctx); err != nil {
		return err
	}
	if err := tctx.BranchWorkspace.ValidateMeta(ctx); err != nil {
		return err
	}
	_, err := os.Lstat(abs)
	return err
}

func workerBranchOrSurveyWalk(
	ctx context.Context,
	tctx tools.ToolContext,
	fullRoot string,
	opts sandbox.SurveyOptions,
	fn func(sandbox.SurveyEntry) (sandbox.SurveyAction, error),
) error {
	if ok, err := workerBranchSurveyWalk(ctx, tctx, fullRoot, opts, fn); ok {
		return err
	}
	return sandbox.SurveyWalk(ctx, fullRoot, opts, fn)
}
