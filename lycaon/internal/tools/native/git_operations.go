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
	if isDirectPrivateGitKind(t.Kind) || t.PrivateGit != nil {
		writeRoot := tools.HostWriteRoot(tc)
		_, finish, managed, prepErr := privategit.PrepareManagerViewOperation(ctx, t.PrivateGit, writeRoot, tc.Identity.WorkerJobID)
		if prepErr != nil {
			return "", prepErr
		}
		defer func() { _ = finish() }()
		if err := workerGitPrecondition(tc, managed, t.PrivateGit); err != nil {
			return "", err
		}
		if isDirectPrivateGitKind(t.Kind) || managed {
			return runPrivateGitExecutorOperation(ctx, t.PrivateGit, writeRoot, tc, t.Kind, req)
		}
	}
	result, err := GitMutations{Git: t.Git, Boundary: t.Boundary, PrivateGit: t.PrivateGit}.Operate(ctx, tc, req)
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
	Git        git.GitManager
	Boundary   *sandbox.Boundary
	PrivateGit *privategit.Manager
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
	if err := workerGitPrecondition(tc, managed, m.PrivateGit); err != nil {
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

// workerGitPrecondition refuses a worker that has neither a branch write root
// nor a private Git view to operate on.
func workerGitPrecondition(tc tools.ToolContext, managed bool, mgr *privategit.Manager) error {
	if tc.Identity.WorkerJobID == "" || managed || strings.TrimSpace(tc.Source.WorkerBranchRoot) != "" || mgr != nil {
		return nil
	}
	return &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "addressed_session_required", "git_addressed_session_required": true}}
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
