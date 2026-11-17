package native

import (
	"context"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// Restore restores explicit active-root paths in the index, the work tree or
// both after host review, returning the restored paths.
func (m GitMutations) Restore(ctx context.Context, tctx tools.ToolContext, opts git.GitRestoreOpts) ([]string, error) {
	hostRoot := tools.HostWriteRoot(tctx)
	gitRoot, finish, _, prepErr := privategit.PrepareViewGitOperation(ctx, hostRoot, tctx.Identity.WorkerJobID)
	if prepErr != nil {
		return nil, prepErr
	}
	defer func() { _ = finish() }()
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
		gitPath, ok := gitRelativeWritePath(hostRoot, resolved.Abs)
		if !ok {
			return nil, &toolrejection.ToolReject{Code: "GIT_RESTORE_PATH_DENIED", Data: map[string]any{"path": relPath}}
		}
		gitPaths = append(gitPaths, gitPath)
		restored = append(restored, filepath.ToSlash(relPath))
	}
	opts.Paths = gitPaths
	opts.Review = func(ctx context.Context, files []git.RestoreFile) error {
		return reviewGitFiles(ctx, tctx, m.Boundary, "git_restore", files)
	}
	if err := m.Git.Restore(ctx, gitRoot, opts); err != nil {
		return nil, err
	}
	if err := finish(); err != nil {
		return nil, err
	}
	for _, path := range restored {
		tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	return restored, nil
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
	cwd, finish, _, prepErr := privategit.PrepareViewGitOperation(ctx, tools.HostWriteRoot(tc), tc.Identity.WorkerJobID)
	if prepErr != nil {
		return "", git.CommitStaging{}, prepErr
	}
	defer func() { _ = finish() }()
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
	if err != nil {
		return hash, staging, err
	}
	if err := finish(); err != nil {
		return "", git.CommitStaging{}, err
	}
	return hash, staging, nil
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
