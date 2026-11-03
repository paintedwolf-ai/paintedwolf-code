package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitRestoreTool restores explicit paths in the index, worktree, or both.
type GitRestoreTool struct {
	Git      git.GitManager
	Boundary *sandbox.Boundary
}

func (t *GitRestoreTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	opts, err := parseGitRestoreOpts(args)
	if err != nil {
		return "", err
	}
	prof := tctx.ProfileID()
	hostRoot := tools.HostWriteRoot(tctx)
	gitRoot, finish, _, prepErr := privategit.PrepareViewGitOperation(ctx, hostRoot, tctx.Identity.WorkerJobID)
	if prepErr != nil {
		return "", prepErr
	}
	defer func() { _ = finish() }()
	gitPaths := make([]string, 0, len(opts.Paths))
	restored := make([]string, 0, len(opts.Paths))
	for _, relPath := range opts.Paths {
		if err := assertGitRestorePath(ctx, t.Boundary, tctx, relPath, prof); err != nil {
			return "", err
		}
		resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, relPath)
		if err != nil {
			return "", err
		}
		gitPath, ok := gitRelativeWritePath(hostRoot, resolved.Abs)
		if !ok {
			return "", &toolrejection.ToolReject{Code: "GIT_RESTORE_PATH_DENIED", Data: map[string]any{"path": relPath}}
		}
		gitPaths = append(gitPaths, gitPath)
		restored = append(restored, filepath.ToSlash(relPath))
	}
	opts.Paths = gitPaths
	_ = privategit.MaterializeViewPaths(ctx, hostRoot, tctx.Identity.WorkerJobID, gitPaths...)
	opts.Review = func(ctx context.Context, files []git.RestoreFile) error { return t.reviewRestore(ctx, tctx, files) }
	if err := t.Git.Restore(ctx, gitRoot, opts); err != nil {
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) || toolrejection.HostRefusal(err) != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		return git.MarshalRestoreToolResponse(nil, err)
	}
	for _, path := range restored {
		tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	return git.MarshalRestoreToolResponse(restored, nil)
}

func parseGitRestoreOpts(args map[string]any) (git.GitRestoreOpts, error) {
	paths, err := parseBoundedPaths(args, git.DefaultGitRestoreMaxPaths, func(max, got int) error {
		return &toolrejection.ToolReject{
			Code: "GIT_RESTORE_BULK_DENIED",
			Data: map[string]any{"max_paths": max, "requested": got},
		}
	})
	if err != nil {
		return git.GitRestoreOpts{}, err
	}
	source, _ := args["source"].(string)
	staged, _ := args["staged"].(bool)
	worktree, _ := args["worktree"].(bool)
	return git.GitRestoreOpts{Paths: paths, Source: source, Staged: staged, Worktree: worktree}, nil
}

func assertGitRestorePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) error {
	_, err := assertWritePath(ctx, boundary, tctx, relPath, profileID, "git_restore", func(path string) error {
		return &toolrejection.ToolReject{Code: "GIT_RESTORE_PATH_DENIED", Data: map[string]any{"path": path}}
	})
	return err
}

func gitRelativeWritePath(hostRoot, resolvedAbs string) (string, bool) {
	if rel, err := filepath.Rel(hostRoot, resolvedAbs); err == nil && !sandbox.HasParentTraversal(rel) {
		return filepath.ToSlash(rel), true
	}
	if canonRoot, err := filepath.EvalSymlinks(hostRoot); err == nil {
		if rel, err := filepath.Rel(canonRoot, resolvedAbs); err == nil && !sandbox.HasParentTraversal(rel) {
			return filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// GitCommitTool stages paths and creates or amends a commit.
type GitCommitTool struct {
	Git      git.GitManager
	Boundary *sandbox.Boundary
}

func (t *GitCommitTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	message, _ := args["message"].(string)
	message = strings.TrimSpace(message)
	if message == "" {
		return "", toolkit.MissingArg("message")
	}
	cwd, finish, _, prepErr := privategit.PrepareViewGitOperation(ctx, tools.HostWriteRoot(tctx), tctx.Identity.WorkerJobID)
	if prepErr != nil {
		return "", prepErr
	}
	defer func() { _ = finish() }()
	staging, err := resolveCommitStaging(ctx, args, t.Git, tctx, cwd)
	if err != nil {
		return "", err
	}
	prof := tctx.ProfileID()
	staged := make([]string, 0, len(staging.Paths))
	for _, relPath := range staging.Paths {
		if err := assertGitCommitPath(ctx, t.Boundary, tctx, relPath, prof); err != nil {
			return "", err
		}
		staged = append(staged, filepath.ToSlash(relPath))
	}
	staging.Paths = staged
	_ = privategit.MaterializeViewPaths(ctx, tools.HostWriteRoot(tctx), tctx.Identity.WorkerJobID, staged...)
	amend, _ := args["amend"].(bool)
	ctx, historyErr := tools.GitHistoryContext(ctx, tctx)
	if historyErr != nil {
		return "", historyErr
	}
	hash, err := t.Git.Commit(ctx, cwd, git.GitCommitOpts{Message: message, Paths: staged, Amend: amend})
	if err != nil {
		return git.MarshalCommitToolResponse(hash, staging, err)
	}
	return git.MarshalCommitToolResponse(hash, staging, nil)
}

func assertGitCommitPath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) error {
	deny := func(path string) error {
		return &toolrejection.ToolReject{Code: "GIT_COMMIT_PATH_DENIED", Data: map[string]any{"path": path}}
	}
	if sandbox.HasParentTraversal(relPath) {
		return deny(filepath.ToSlash(relPath))
	}
	resolved, err := projectpaths.ResolveGitStage(ctx, boundary, tctx, relPath)
	if err == nil {
		err = assertResolvedProfileWriteScope(ctx, boundary, tctx, resolved, relPath, "git_commit")
	}
	if err != nil {
		return writePathReject(ctx, boundary, filepath.ToSlash(relPath), profileID, "git_commit", err, deny)
	}
	return err
}
