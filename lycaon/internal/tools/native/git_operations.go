package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitOperationTool binds repository effects to source scope and prepared review.
type GitOperationTool struct {
	Git        git.GitManager
	PrivateGit *privategit.Manager
	Boundary   *sandbox.Boundary
	Kind       string
}

func (t *GitOperationTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	req := git.OperationRequest{Kind: t.Kind, Action: gitString(args, "action"), Branch: gitString(args, "branch"), Ref: gitString(args, "ref"), Mode: gitString(args, "mode"), Message: gitString(args, "message"), Paths: gitOperationPaths(args)}
	req.Create, _ = args["create"].(bool)
	if commit, ok := args["commit"].(bool); ok {
		req.NoCommit = !commit
	}
	req.IncludeUntracked, _ = args["include_untracked"].(bool)
	req.ReinstateIndex, _ = args["reinstate_index"].(bool)

	writeRoot := tools.HostWriteRoot(tc)
	cwd, finish, managed, prepErr := privategit.PrepareManagerViewOperation(ctx, t.PrivateGit, writeRoot, tc.Identity.WorkerJobID)
	if prepErr != nil {
		return "", prepErr
	}
	defer func() { _ = finish() }()

	if tc.Identity.WorkerJobID != "" && !managed && strings.TrimSpace(tc.Source.WorkerBranchRoot) == "" && t.PrivateGit == nil {
		return "", &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "addressed_session_required", "git_addressed_session_required": true}}
	}

	if isDirectPrivateGitKind(t.Kind) || (managed && t.PrivateGit != nil) {
		return runPrivateGitExecutorOperation(ctx, t.PrivateGit, writeRoot, tc, t.Kind, req)
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
	result, err := t.Git.Operate(ctx, cwd, req)
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

func isDirectPrivateGitKind(kind string) bool {
	switch kind {
	case "revert", "switch", "worktree", "submodule", "status", "diff", "log", "commit":
		return true
	}
	return false
}

func runPrivateGitExecutorOperation(
	ctx context.Context,
	mgr *privategit.Manager,
	writeRoot string,
	tc tools.ToolContext,
	kind string,
	req git.OperationRequest,
) (string, error) {
	exec, ok := privategit.LookupExecutor(mgr, writeRoot, tc.Identity.WorkerJobID)
	if !ok {
		return "", fmt.Errorf("private git executor unavailable for kind %q", kind)
	}
	var out string
	var err error
	switch kind {
	case "status":
		out, err = exec.Status(ctx, "--short", "--branch")
	case "diff":
		out, err = exec.Diff(ctx, req.Paths...)
	case "log":
		ref := req.Ref
		if ref == "" {
			ref = "HEAD"
		}
		out, err = exec.Log(ctx, "--oneline", "-n", "10", ref)
	case "commit":
		if len(req.Paths) > 0 {
			_, _ = exec.Run(ctx, ".", append([]string{"add", "--"}, req.Paths...)...)
			out, err = exec.Commit(ctx, req.Message)
		} else {
			out, err = exec.Commit(ctx, req.Message, "-a")
		}
	case "revert":
		ref := req.Ref
		if ref == "" {
			ref = "HEAD"
		}
		out, err = exec.Revert(ctx, ref)
	case "switch":
		var swArgs []string
		if req.Create {
			swArgs = append(swArgs, "-c")
		}
		if req.Branch != "" {
			swArgs = append(swArgs, req.Branch)
		}
		if req.Ref != "" {
			swArgs = append(swArgs, req.Ref)
		}
		out, err = exec.Switch(ctx, swArgs...)
	case "checkout":
		var coArgs []string
		if req.Create {
			coArgs = append(coArgs, "-b")
		}
		if req.Branch != "" {
			coArgs = append(coArgs, req.Branch)
		}
		if req.Ref != "" {
			coArgs = append(coArgs, req.Ref)
		}
		out, err = exec.Checkout(ctx, coArgs...)
	case "stash":
		switch req.Action {
		case "apply", "drop", "pop":
			ref := req.Ref
			if ref == "" {
				ref = "stash@{0}"
			}
			out, err = exec.Stash(ctx, req.Action, ref)
		case "list":
			out, err = exec.Stash(ctx, "list")
		default:
			stArgs := []string{"push"}
			if req.IncludeUntracked {
				stArgs = append(stArgs, "-u")
			}
			if req.Message != "" {
				stArgs = append(stArgs, "-m", req.Message)
			}
			if len(req.Paths) > 0 {
				stArgs = append(stArgs, "--")
				stArgs = append(stArgs, req.Paths...)
			}
			out, err = exec.Stash(ctx, stArgs...)
		}
	case "worktree":
		out, err = exec.WorktreeList(ctx)
	case "submodule":
		out, err = exec.SubmoduleStatus(ctx)
	default:
		out, err = exec.Run(ctx, ".", kind)
	}
	if err != nil {
		return "", err
	}
	raw, mErr := surveyjson.Marshal(map[string]any{
		"available": true,
		"status":    "completed",
		"kind":      kind,
		"output":    strings.TrimSpace(out),
	})
	return string(raw), mErr
}

func gitString(args map[string]any, key string) string { value, _ := args[key].(string); return value }

// GitCompareTool reports graph facts for two immutable resolved tips.
type GitCompareTool struct {
	Git        git.GitManager
	PrivateGit *privategit.Manager
}

func (t *GitCompareTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	head := gitString(args, "head_ref")
	if head == "" {
		head = "HEAD"
	}
	cwd, finish, _, prepErr := privategit.PrepareManagerViewOperation(ctx, t.PrivateGit, tools.HostWriteRoot(tc), tc.Identity.WorkerJobID)
	if prepErr != nil {
		return git.MarshalToolFailure(prepErr)
	}
	defer func() { _ = finish() }()
	result, err := t.Git.Compare(ctx, cwd, gitString(args, "base_ref"), head)
	if err != nil {
		return git.MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(result)
	return string(raw), err
}

// GitStashListTool exposes a bounded page with stable object identities.
type GitStashListTool struct {
	Git        git.GitManager
	PrivateGit *privategit.Manager
}

func (t *GitStashListTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	offset := intArg(args, "offset", 0)
	limit := intArg(args, "limit", 20)
	if limit > git.MaxGitLogPage {
		limit = git.MaxGitLogPage
	}
	cwd, finish, _, prepErr := privategit.PrepareManagerViewOperation(ctx, t.PrivateGit, tools.HostWriteRoot(tc), tc.Identity.WorkerJobID)
	if prepErr != nil {
		return git.MarshalToolFailure(prepErr)
	}
	defer func() { _ = finish() }()
	entries, err := t.Git.ListStashes(ctx, cwd, offset, limit+1)
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
