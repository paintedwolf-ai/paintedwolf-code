package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
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
	if err == nil && show != nil && !show.Stat {
		if show.Kind == "blob" {
			abs, _, resolveErr := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, show.Path)
			if resolveErr != nil {
				return "", resolveErr
			}
			if deliveryErr := tctx.ObserveCredentialDelivery(ctx, abs, show.Path, show.Content); deliveryErr != nil {
				return "", deliveryErr
			}
		} else if deliveryErr := deliverGitPatchCredentials(ctx, tctx, show.Content); deliveryErr != nil {
			return "", deliveryErr
		}
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
	if err == nil {
		var content strings.Builder
		for _, line := range lines {
			content.WriteString(line.Content)
			content.WriteByte('\n')
		}
		abs, _, resolveErr := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, path)
		if resolveErr != nil {
			return "", resolveErr
		}
		if deliveryErr := tctx.ObserveCredentialDelivery(ctx, abs, path, content.String()); deliveryErr != nil {
			return "", deliveryErr
		}
	}
	return git.MarshalBlameToolResponse(lines, err)
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
	restored, err := GitMutations{Git: t.Git, Boundary: t.Boundary}.Restore(ctx, tctx, opts)
	if err != nil {
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) || toolrejection.HostRefusal(err) != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		return git.MarshalRestoreToolResponse(nil, err)
	}
	return git.MarshalRestoreToolResponse(restored, nil)
}

// Restore restores explicit active-root paths in the index, the work tree or
// both after host review, returning the restored paths.
func (m GitMutations) Restore(ctx context.Context, tctx tools.ToolContext, opts git.GitRestoreOpts) ([]string, error) {
	gitRoot := tools.HostWriteRoot(tctx)
	gitPaths := make([]string, 0, len(opts.Paths))
	restored := make([]string, 0, len(opts.Paths))
	for _, relPath := range opts.Paths {
		if err := assertGitRestorePath(ctx, m.Boundary, tctx, relPath, tctx.ProfileID()); err != nil {
			return nil, err
		}
		resolved, err := projectpaths.ResolveWrite(ctx, m.Boundary, tctx, relPath)
		if err != nil {
			return nil, err
		}
		gitPath, err := filepath.Rel(gitRoot, resolved.Abs)
		if err != nil || sandbox.HasParentTraversal(gitPath) {
			return nil, &toolrejection.ToolReject{Code: "GIT_RESTORE_PATH_DENIED", Data: map[string]any{"path": relPath}}
		}
		gitPaths = append(gitPaths, filepath.ToSlash(gitPath))
		restored = append(restored, filepath.ToSlash(relPath))
	}
	opts.Paths = gitPaths
	opts.Review = func(ctx context.Context, files []git.RestoreFile) error {
		return reviewGitFiles(ctx, tctx, m.Boundary, "git_restore", files)
	}
	if err := m.Git.Restore(ctx, gitRoot, opts); err != nil {
		return nil, err
	}
	for _, path := range restored {
		tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	return restored, nil
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
	request := GitCommitRequest{Message: message}
	request.Amend, _ = args["amend"].(bool)
	if raw, ok := args["paths"].([]any); ok && len(raw) > 0 {
		paths, err := parseBoundedPaths(args, git.DefaultGitCommitMaxPaths, commitBulkDenied)
		if err != nil {
			return "", err
		}
		request.Paths = paths
	}
	hash, staging, err := GitMutations{Git: t.Git, Boundary: t.Boundary}.Commit(ctx, tctx, request)
	if hash == "" && staging.Paths == nil && err != nil {
		return "", err
	}
	return git.MarshalCommitToolResponse(hash, staging, err)
}

// GitCommitRequest is one commit: explicit paths stay explicit; without
// them the session's authored paths, plus any it staged itself, are
// committed and unrelated staged work is left as it is.
type GitCommitRequest struct {
	Message string
	Paths   []string
	// Staged are paths this session staged through git add.
	Staged []string
	Amend  bool
	// IdentityName and IdentityEmail override the recorded author and
	// committer for this commit, as git -c user.name/user.email does.
	IdentityName, IdentityEmail string
}

// Commit admits, records and creates or amends one commit.
func (m GitMutations) Commit(ctx context.Context, tc tools.ToolContext, request GitCommitRequest) (string, git.CommitStaging, error) {
	cwd := tc.ActiveRootPath()
	var staging git.CommitStaging
	switch {
	case len(request.Paths) > 0:
		staging = git.CommitStaging{Paths: request.Paths}
	case request.Amend && len(request.Staged) == 0:
		// A message-only amend commits no paths.
	default:
		var err error
		staging, err = sessionCommitStaging(ctx, m.Git, tc, cwd, request.Staged)
		if err != nil {
			return "", git.CommitStaging{}, err
		}
	}
	staged := make([]string, 0, len(staging.Paths))
	for _, relPath := range staging.Paths {
		if err := assertGitCommitPath(ctx, m.Boundary, tc, relPath, tc.ProfileID()); err != nil {
			return "", git.CommitStaging{}, err
		}
		staged = append(staged, filepath.ToSlash(relPath))
	}
	staging.Paths = staged
	ctx, historyErr := tools.GitHistoryContext(ctx, tc)
	if historyErr != nil {
		return "", git.CommitStaging{}, historyErr
	}
	hash, err := m.Git.Commit(ctx, cwd, git.GitCommitOpts{Message: request.Message, Paths: staged, Amend: request.Amend,
		IdentityName: request.IdentityName, IdentityEmail: request.IdentityEmail})
	return hash, staging, err
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

func intArg(args map[string]any, key string, defaultVal int) int {
	if v, ok := args[key].(float64); ok && v > 0 {
		return int(v)
	}
	return defaultVal
}
