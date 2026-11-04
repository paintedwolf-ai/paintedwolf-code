package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitOperationTool binds repository effects to source scope and prepared review.
type GitOperationTool struct {
	Git      git.GitManager
	Boundary *sandbox.Boundary
	Kind     string
}

func (t *GitOperationTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	req := git.OperationRequest{Kind: t.Kind, Action: gitString(args, "action"), Branch: gitString(args, "branch"), Ref: gitString(args, "ref"), Mode: gitString(args, "mode"), Message: gitString(args, "message"), Paths: gitOperationPaths(args)}
	req.Create, _ = args["create"].(bool)
	if commit, ok := args["commit"].(bool); ok {
		req.NoCommit = !commit
	}
	req.IncludeUntracked, _ = args["include_untracked"].(bool)
	req.ReinstateIndex, _ = args["reinstate_index"].(bool)
	result, err := GitMutations{Git: t.Git, Boundary: t.Boundary}.Operate(ctx, tc, req)
	if err != nil && !result.Attempted {
		return "", err
	}
	if err != nil {
		result.Status = "failed"
		result.Diagnostics += "\n" + err.Error()
	}
	output, marshalErr := git.MarshalOperationResult(result)
	if marshalErr != nil {
		return "", errors.Join(err, marshalErr)
	}
	if result.Status == "failed" || result.Status == "conflicts" {
		return output, &toolrejection.ToolReject{Code: "GIT_OPERATION_FAILED", FailureClass: api.FailureClassOwnerError, Data: map[string]any{
			"status": result.Status, "attempted": result.Attempted, "exit_code": result.ExitCode,
			"git_after_observed": result.AfterObserved,
			"git_merge_active":   result.AfterObserved && result.After.MergeHead != "",
			"git_conflict_paths": append([]string(nil), result.After.Conflicts...),
		}}
	}
	return output, nil
}

// GitMutations is the one mutation path the structured Git tools and the git
// command forms share: path admission, host review of every file effect,
// source history, and the Git owner's rehearsed, revalidated operation.
type GitMutations struct {
	Git      git.GitManager
	Boundary *sandbox.Boundary
}

// Operate runs one reviewed repository operation in the active root. A
// precondition refusal returns before any effect; an attempted operation
// returns its observed result, with an error only when Git could not finish.
func (m GitMutations) Operate(ctx context.Context, tc tools.ToolContext, req git.OperationRequest) (git.OperationResult, error) {
	if tc.Identity.WorkerJobID != "" {
		return git.OperationResult{}, &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "addressed_session_required", "git_addressed_session_required": true}}
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
	result, err := m.Git.Operate(ctx, root.Abs, req)
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
	return result, err
}

func gitString(args map[string]any, key string) string { value, _ := args[key].(string); return value }

func assertGitOperationPath(ctx context.Context, boundary *sandbox.Boundary, tc tools.ToolContext, path, tool string) error {
	_, err := assertWritePath(ctx, boundary, tc, path, tc.ProfileID(), tool, func(path string) error {
		return &toolrejection.ToolReject{Code: "GIT_PATH_DENIED", Data: map[string]any{"path": filepath.ToSlash(path), "tool": tool}}
	})
	return err
}

func gitOperationPaths(args map[string]any) []string {
	if paths, ok := args["paths"].([]string); ok {
		return append([]string(nil), paths...)
	}
	values, _ := args["paths"].([]any)
	paths := make([]string, 0, len(values))
	for _, value := range values {
		if path, ok := value.(string); ok {
			paths = append(paths, path)
		}
	}
	return paths
}
