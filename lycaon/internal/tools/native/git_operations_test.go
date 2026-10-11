package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestGitCheckoutReviewsInstructionFilesBeforeChangingBranch(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	gittest.Run(t, dir, "config", "user.name", "Test")
	gittest.Run(t, dir, "config", "user.email", "test@example.com")
	// The host treats case variants as instruction files on every platform.
	path := filepath.Join(dir, "content", "sdk", "agents.md")
	testutil.FailErr(t, "create docs directory", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write initial policy", os.WriteFile(path, []byte("first\n"), 0o644))
	gittest.Run(t, dir, "add", "--", "content/sdk/agents.md")
	gittest.Run(t, dir, "commit", "-m", "Initial")
	gittest.Run(t, dir, "branch", "-M", "main")
	gittest.Run(t, dir, "checkout", "-b", "feature")
	testutil.FailErr(t, "change policy", os.WriteFile(path, []byte("second\n"), 0o644))
	gittest.Run(t, dir, "add", "--", "content/sdk/agents.md")
	gittest.Run(t, dir, "commit", "-m", "Feature")
	tool := GitOperationTool{Git: git.NewManager(), Boundary: nativefixture.Boundary(t), Kind: "checkout"}
	tc := nativefixture.Context(dir)
	refused := errors.New("review declined")
	reviewed := false
	tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		reviewed = true
		if len(changes) != 2 || changes[0].Path != path || changes[0].Preview.Before != "second\n" || changes[0].Preview.After != "first\n" {
			t.Fatalf("wrong instruction preview: %+v", changes)
		}
		return refused
	}
	_, err := tool.Run(t.Context(), map[string]any{"branch": "main"}, tc)
	if !reviewed || !errors.Is(err, refused) {
		t.Fatalf("approval did not protect checkout: %v reviewed=%v", err, reviewed)
	}
	if branch := gittest.Run(t, dir, "branch", "--show-current"); branch != "feature\n" {
		t.Fatalf("branch changed before approval: %q", branch)
	}
	tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { return nil }
	_, err = tool.Run(t.Context(), map[string]any{"branch": "main"}, tc)
	testutil.FailErr(t, "approved checkout", err)
	if branch := gittest.Run(t, dir, "branch", "--show-current"); branch != "main\n" {
		t.Fatalf("checkout did not finish: %q", branch)
	}
}

type partialOperationGit struct{ git.GitManager }

func (partialOperationGit) Operate(context.Context, string, git.OperationRequest) (git.OperationResult, error) {
	return git.OperationResult{Status: "conflicts", Attempted: true, AfterObserved: true, After: git.RepositoryState{MergeHead: "retained-merge", Conflicts: []string{"file.txt"}}, Paths: []string{"file.txt"}}, nil
}

func TestGitPartialOperationKeepsStateAndRaisesPolicyOccurrence(t *testing.T) {
	tool := GitOperationTool{Git: partialOperationGit{}, Boundary: nativefixture.Boundary(t), Kind: "merge"}
	out, err := tool.Run(t.Context(), map[string]any{}, nativefixture.Context(t.TempDir()))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_OPERATION_FAILED" {
		t.Fatalf("partial operation lost failure occurrence: %v", err)
	}
	var result git.OperationResult
	testutil.FailErr(t, "decode retained partial state", json.Unmarshal([]byte(out), &result))
	if !result.Attempted || result.After.MergeHead != "retained-merge" || len(result.After.Conflicts) != 1 {
		t.Fatalf("partial state lost: %+v", result)
	}
	if reject.Data["git_merge_active"] != true || reject.Data["git_after_observed"] != true {
		t.Fatalf("recovery lacks observed state: %#v", reject.Data)
	}
}
