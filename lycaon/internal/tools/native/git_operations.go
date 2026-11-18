package native

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitMutations is the mutation path of the git command forms: path admission,
// host review of every file effect, source history, and the Git owner's
// rehearsed, revalidated operation.
type GitMutations struct {
	Git        git.GitManager
	Boundary   *sandbox.Boundary
	PrivateGit *privategit.Manager
	// BeforeWorktree publishes admitted inputs before Git inspects the tree.
	BeforeWorktree func(context.Context) error
}

// Operate runs one reviewed repository operation in the active root. A
// precondition refusal returns before any effect; an attempted operation
// returns its observed result, with an error only when Git could not finish.
func (m GitMutations) Operate(ctx context.Context, tc tools.ToolContext, req git.OperationRequest) (git.OperationResult, error) {
	cwd, finish, managed, prepErr := privategit.PrepareManagerViewOperation(ctx, m.PrivateGit, tools.HostWriteRoot(tc), tc.Identity.WorkerJobID)
	if prepErr != nil {
		return git.OperationResult{}, prepErr
	}
	defer func() { _ = finish() }()
	if err := workerGitPrecondition(tc, managed); err != nil {
		return git.OperationResult{}, err
	}
	root, err := projectpaths.ResolveGitStage(ctx, m.Boundary, tc, ".")
	if err != nil {
		return git.OperationResult{}, err
	}
	tool := "git_" + req.Kind
	for _, path := range req.Paths {
		if filepath.Clean(path) == "." {
			continue
		}
		if _, err := projectpaths.ResolveGitStage(ctx, m.Boundary, tc, path); err != nil {
			return git.OperationResult{}, err
		}
		if err := assertGitCommitPath(ctx, m.Boundary, tc, path, tc.ProfileID()); err != nil {
			return git.OperationResult{}, err
		}
	}
	req.Review = func(ctx context.Context, files []git.RestoreFile) error {
		return reviewGitFiles(ctx, tc, m.Boundary, tool, files)
	}
	ctx, historyErr := tools.GitHistoryContext(ctx, tc)
	if historyErr != nil {
		return git.OperationResult{}, historyErr
	}
	target := root.Abs
	if managed {
		target = cwd
	}
	if m.BeforeWorktree != nil {
		if err := m.BeforeWorktree(ctx); err != nil {
			return git.OperationResult{}, err
		}
	}
	result, err := m.Git.Operate(ctx, target, req)
	if err != nil && !result.Attempted {
		var precondition *git.OperationError
		if errors.As(err, &precondition) {
			paths := precondition.Paths[:min(len(precondition.Paths), 80)]
			return result, &toolrejection.ToolReject{Code: precondition.Code(), Data: map[string]any{"reason": precondition.Reason, "paths": precondition.Paths, "path": strings.Join(paths, ", "), "tool": tool}}
		}
		return result, err
	}
	for _, path := range result.Paths {
		tc.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	if err == nil && result.Status != "failed" && result.Status != "conflicts" {
		if finErr := finish(); finErr != nil {
			return result, finErr
		}
	}
	return result, err
}

// workerGitPrecondition admits a worker's Git only through its managed prepared view. A session has
// no such constraint: its repository effects run in its own addressed root.
func workerGitPrecondition(tc tools.ToolContext, managed bool) error {
	if tc.Identity.WorkerJobID == "" || managed {
		return nil
	}
	return &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "addressed_session_required", "git_addressed_session_required": true}}
}

func assertGitOperationPath(ctx context.Context, boundary *sandbox.Boundary, tc tools.ToolContext, path, tool string) error {
	_, err := assertWritePath(ctx, boundary, tc, path, tc.ProfileID(), tool, func(path string) error {
		return &toolrejection.ToolReject{Code: "GIT_PATH_DENIED", Data: map[string]any{"path": filepath.ToSlash(path), "tool": tool}}
	})
	return err
}
