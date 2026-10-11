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
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
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
	if tc.Identity.WorkerJobID != "" {
		return "", &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "addressed_session_required", "git_addressed_session_required": true}}
	}
	tool := "git_" + t.Kind
	for _, path := range req.Paths {
		if _, err := projectpaths.ResolveGitStage(ctx, t.Boundary, tc, path); err != nil {
			return "", err
		}
		if err := assertGitCommitPath(ctx, t.Boundary, tc, path, tc.ProfileID()); err != nil {
			return "", err
		}
	}
	req.Review = func(ctx context.Context, files []git.RestoreFile) error {
		return reviewGitFiles(ctx, tc, t.Boundary, tool, files)
	}
	ctx, historyErr := tools.GitHistoryContext(ctx, tc)
	if historyErr != nil {
		return "", historyErr
	}
	result, err := t.Git.Operate(ctx, tc.ActiveRootPath(), req)
	if err != nil && !result.Attempted {
		var precondition *git.OperationError
		if errors.As(err, &precondition) {
			paths := precondition.Paths[:min(len(precondition.Paths), 80)]
			return "", &toolrejection.ToolReject{Code: precondition.Code(), Data: map[string]any{"reason": precondition.Reason, "paths": precondition.Paths, "path": strings.Join(paths, ", "), "tool": tool}}
		}
		return "", err
	}
	if err != nil {
		result.Status = "failed"
		result.Diagnostics += "\n" + err.Error()
	}
	for _, path := range result.Paths {
		tc.RecordSourcePath(path, api.NavigationEntryKindFile)
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

func gitString(args map[string]any, key string) string { value, _ := args[key].(string); return value }

// GitCompareTool reports graph facts for two immutable resolved tips.
type GitCompareTool struct{ Git git.GitManager }

func (t *GitCompareTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	head := gitString(args, "head_ref")
	if head == "" {
		head = "HEAD"
	}
	result, err := t.Git.Compare(ctx, tc.ActiveRootPath(), gitString(args, "base_ref"), head)
	if err != nil {
		return git.MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(result)
	return string(raw), err
}

// GitStashListTool exposes a bounded page with stable object identities.
type GitStashListTool struct{ Git git.GitManager }

func (t *GitStashListTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	offset := intArg(args, "offset", 0)
	limit := intArg(args, "limit", 20)
	if limit > git.MaxGitLogPage {
		limit = git.MaxGitLogPage
	}
	entries, err := t.Git.ListStashes(ctx, tc.ActiveRootPath(), offset, limit+1)
	if err != nil {
		return git.MarshalToolFailure(err)
	}
	var next *int
	if len(entries) > limit {
		entries = entries[:limit]
		n := offset + limit
		next = &n
	}
	raw, err := surveyjson.Marshal(struct {
		Available  bool             `json:"available"`
		Stashes    []git.StashEntry `json:"stashes"`
		NextOffset *int             `json:"next_offset,omitempty"`
	}{true, entries, next})
	return string(raw), err
}

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
