package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitShowTool shows commit or file content at a ref.
type GitShowTool struct {
	Git      git.GitManager
	Boundary *sandbox.Boundary
}

func (t *GitShowTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	ref, _ := args["ref"].(string)
	path, _ := args["path"].(string)
	stat, _ := args["stat"].(bool)
	maxBytes := intArg(args, "max_bytes", git.DefaultGitShowMaxBytes)
	cwd := tctx.ActiveRootPath()
	if path = strings.TrimSpace(path); path != "" {
		if err := assertGitReadPath(ctx, t.Boundary, tctx, path, tctx.ProfileID(), "git_show"); err != nil {
			return "", err
		}
	}
	show, err := t.Git.Show(ctx, cwd, git.GitShowOpts{
		Ref:      ref,
		Path:     path,
		MaxBytes: maxBytes,
		Stat:     stat,
	})
	if err == nil && path != "" {
		tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	return git.MarshalShowToolResponse(show, err)
}

// GitBlameTool attributes lines in a file.
type GitBlameTool struct {
	Git      git.GitManager
	Boundary *sandbox.Boundary
}

func (t *GitBlameTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	path, _ := args["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return "", toolkit.MissingArg("path")
	}
	if err := assertGitReadPath(ctx, t.Boundary, tctx, path, tctx.ProfileID(), "git_blame"); err != nil {
		return "", err
	}
	lines, err := t.Git.Blame(ctx, tctx.ActiveRootPath(), git.GitBlameOpts{
		Path:      path,
		StartLine: intArg(args, "start_line", 0),
		EndLine:   intArg(args, "end_line", 0),
		MaxLines:  intArg(args, "max_lines", git.DefaultGitBlameMaxLines),
	})
	if err == nil {
		tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	return git.MarshalBlameToolResponse(lines, err)
}

// GitRefTool resolves refs to SHAs.
type GitRefTool struct {
	Git git.GitManager
}

func (t *GitRefTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	refs := stringSliceFromArg(args, "refs")
	if len(refs) == 0 {
		return "", toolkit.MissingArg("refs")
	}
	out, err := t.Git.RevParse(ctx, tctx.ActiveRootPath(), refs)
	return git.MarshalRefToolResponse(out, err)
}

// GitBranchesTool lists local branches.
type GitBranchesTool struct {
	Git git.GitManager
}

func (t *GitBranchesTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	max := intArg(args, "max_branches", git.DefaultGitBranchesMax)
	branches, err := t.Git.Branches(ctx, tctx.ActiveRootPath(), max)
	return git.MarshalBranchesToolResponse(branches, err)
}

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
	gitRoot := tools.HostWriteRoot(tctx)
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
		gitPath, err := filepath.Rel(gitRoot, resolved.Abs)
		if err != nil || sandbox.HasParentTraversal(gitPath) {
			return "", &toolrejection.ToolReject{Code: "GIT_RESTORE_PATH_DENIED", Data: map[string]any{"path": relPath}}
		}
		gitPaths = append(gitPaths, filepath.ToSlash(gitPath))
		restored = append(restored, filepath.ToSlash(relPath))
	}
	opts.Paths = gitPaths
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

func assertGitReadPath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID, tool string,
) error {
	if sandbox.HasParentTraversal(relPath) {
		return fmt.Errorf("path contains path escape")
	}
	relSlash := filepath.ToSlash(relPath)
	if tools.IsSensitivePath(relSlash) {
		return &toolrejection.ToolReject{Code: "GIT_PATH_DENIED", Data: map[string]any{"path": relSlash, "tool": tool}}
	}
	if boundary == nil {
		return nil
	}
	if _, err := projectpaths.ResolveRead(ctx, boundary, tctx, relPath); err != nil {
		// Preserve the resolver's structured rejection.
		var structured *toolrejection.ToolReject
		if errors.As(err, &structured) || toolrejection.HostRefusal(err) != nil {
			return err
		}
		return &toolrejection.ToolReject{Code: "GIT_PATH_DENIED", Data: map[string]any{"path": relSlash, "tool": tool}}
	}
	return nil
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
	cwd := tctx.ActiveRootPath()
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

func stringSliceFromArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func intArg(args map[string]any, key string, defaultVal int) int {
	if v, ok := args[key].(float64); ok && v > 0 {
		return int(v)
	}
	return defaultVal
}
