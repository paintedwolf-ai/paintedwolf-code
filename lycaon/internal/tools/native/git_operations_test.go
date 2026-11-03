package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/sandbox"
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

func TestGitMutationAdmitsRootBeforePathlessOrDotOperations(t *testing.T) {
	for _, paths := range [][]string{nil, {"."}} {
		tc := nativefixture.Context(t.TempDir())
		tc.Source.Roots = nil
		_, err := (GitMutations{Git: partialOperationGit{}, Boundary: nativefixture.Boundary(t)}).Operate(t.Context(), tc, git.OperationRequest{Kind: "merge", Paths: paths})
		if rejection := toolrejection.AsToolReject(err); rejection == nil || rejection.Code != "PROJECT_HAS_NO_ROOTS" {
			t.Fatalf("paths %v root admission = %v", paths, err)
		}
	}
}

func TestGitOperationsOnWorkerWriteLegsAcrossAdapters(t *testing.T) {
	for _, adapter := range []string{"fuselinux", "nfsdarwin", "eagercopy"} {
		t.Run(adapter, func(t *testing.T) {
			verifyWorkerWriteLegGitTools(t, adapter)
		})
	}
}

func setupAdapterWorkerView(t *testing.T, trunkDir, adapter string) (*privategit.Manager, string, string, func(string, string)) {
	t.Helper()
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "new privategit manager", err)
	viewID := "view-" + adapter
	workerID := "worker-" + adapter
	t.Cleanup(func() {
		_ = mgr.ReleaseView(viewID)
		_ = mgr.Store().Close()
	})
	if adapter == "eagercopy" {
		targetDir := filepath.Join(t.TempDir(), "materialized-wt")
		_, err = mgr.InitializeMaterializedSeed(t.Context(), privategit.MaterializedSeedConfig{
			ViewID: viewID, WorkerID: workerID, TrunkDir: trunkDir, TargetDir: targetDir,
		})
		testutil.FailErr(t, "initialize materialized seed", err)
		return mgr, workerID, targetDir, func(rel, content string) {
			testutil.FailErr(t, "write materialized file", os.WriteFile(filepath.Join(targetDir, rel), []byte(content), 0o644))
		}
	}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: viewID, BackingDir: filepath.Join(t.TempDir(), "core-"+adapter), GitInterceptor: mgr,
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
	})
	testutil.FailErr(t, "layeredfs.New", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	wtDoubleDir := filepath.Join(t.TempDir(), "unit-wt-double-"+adapter)
	testutil.FailErr(t, "mkdir wtDoubleDir", os.MkdirAll(wtDoubleDir, 0o755))
	readmeBytes, rErr := os.ReadFile(filepath.Join(trunkDir, "README.md"))
	testutil.FailErr(t, "read trunk README.md", rErr)
	testutil.FailErr(t, "seed wtDoubleDir README.md", os.WriteFile(filepath.Join(wtDoubleDir, "README.md"), readmeBytes, 0o644))
	testutil.FailErr(t, "MountedClaimWithBinding", mgr.MountedClaimWithBinding(privategit.MountedViewBinding{
		ViewID: viewID, WorkerID: workerID, RootID: "primary",
		TrunkDir: trunkDir, MountPath: wtDoubleDir, AdapterID: adapter,
		Core: core, MountID: "unit-mount-" + adapter, Active: true,
	}))
	wtDir, finish, _, err := privategit.PrepareManagerViewOperation(t.Context(), mgr, trunkDir, workerID)
	testutil.FailErr(t, "PrepareManagerViewOperation", err)
	_ = finish()
	return mgr, workerID, wtDir, func(rel, content string) {
		_, wErr := core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: rel}, []byte(content), 0o644)
		testutil.FailErr(t, "write core overlay file", wErr)
	}
}

func verifyWorkerWriteLegGitTools(t *testing.T, adapter string) {
	trunkDir := t.TempDir()
	gittest.Init(t, trunkDir)
	gittest.Run(t, trunkDir, "config", "user.name", "Test")
	gittest.Run(t, trunkDir, "config", "user.email", "test@example.com")
	testutil.FailErr(t, "write readme", os.WriteFile(filepath.Join(trunkDir, "README.md"), []byte("hello\n"), 0o644))
	gittest.Run(t, trunkDir, "add", "--", "README.md")
	gittest.Run(t, trunkDir, "commit", "-m", "initial")
	gittest.Run(t, trunkDir, "branch", "-M", "main")
	trunkHead := strings.TrimSpace(gittest.Run(t, trunkDir, "rev-parse", "HEAD"))

	mgr, workerID, wtDir, writeViewFile := setupAdapterWorkerView(t, trunkDir, adapter)
	tc := nativefixture.Context(wtDir)
	tc.Identity.WorkerJobID = workerID
	tc.Source.WorkerBranchRoot = wtDir
	tc.Source.BranchWorkspace = testutil.CompleteBranchWorkspace{}
	boundary := nativefixture.Boundary(t)
	gitMgr := git.NewManager()

	writeViewFile("README.md", "hello worker\n")
	runOp := func(kind string, args map[string]any) string {
		t.Helper()
		tool := GitOperationTool{Git: gitMgr, Boundary: boundary, PrivateGit: mgr, Kind: kind}
		out, opErr := tool.Run(t.Context(), args, tc)
		testutil.FailErr(t, "git_"+kind, opErr)
		return out
	}

	if out := runOp("status", map[string]any{}); !strings.Contains(out, "README.md") {
		t.Fatalf("status missing modified file: %s", out)
	}
	if out := runOp("diff", map[string]any{}); !strings.Contains(out, "+hello worker") {
		t.Fatalf("diff missing worker edit: %s", out)
	}
	runOp("checkout", map[string]any{"branch": "worker-feature", "create": true})
	runOp("commit", map[string]any{"message": "worker commit"})
	if out := runOp("log", map[string]any{"limit": 2}); !strings.Contains(out, "worker commit") {
		t.Fatalf("log missing worker commit: %s", out)
	}

	runOp("revert", map[string]any{"ref": "HEAD"})
	runOp("switch", map[string]any{"branch": "main"})
	runOp("switch", map[string]any{"branch": "worker-feature"})

	writeViewFile("README.md", "stashed edit\n")
	runOp("stash", map[string]any{"action": "save", "message": "wip-stash", "paths": []any{"README.md"}})
	if out := runOp("stash", map[string]any{"action": "list"}); !strings.Contains(out, "stash@{0}") {
		t.Fatalf("stash list missing entry: %s", out)
	}
	runOp("stash", map[string]any{"action": "apply", "ref": "stash@{0}"})
	runOp("stash", map[string]any{"action": "drop", "ref": "stash@{0}"})
	runOp("worktree", map[string]any{})
	runOp("submodule", map[string]any{})

	verifyIntrospectionCommitAndRestoreTools(t, tc, gitMgr, boundary, writeViewFile)

	if gotTrunkHead := strings.TrimSpace(gittest.Run(t, trunkDir, "rev-parse", "HEAD")); gotTrunkHead != trunkHead {
		t.Fatalf("trunk HEAD mutated by worker write leg: got %s, want %s", gotTrunkHead, trunkHead)
	}
}

func verifyIntrospectionCommitAndRestoreTools(
	t *testing.T,
	tc tools.ToolContext,
	gitMgr git.GitManager,
	boundary *sandbox.Boundary,
	writeViewFile func(string, string),
) {
	t.Helper()
	writeViewFile("README.md", "introspection commit line\n")
	commitTool := &GitCommitTool{Git: gitMgr, Boundary: boundary}
	commitOut, err := commitTool.Run(t.Context(), map[string]any{
		"message": "introspection tool commit",
		"paths":   []any{"README.md"},
	}, tc)
	testutil.FailErr(t, "GitCommitTool.Run", err)
	if !strings.Contains(commitOut, "README.md") {
		t.Fatalf("expected README.md in GitCommitTool output: %s", commitOut)
	}

	showTool := &GitShowTool{Git: gitMgr, Boundary: boundary}
	showOut, err := showTool.Run(t.Context(), map[string]any{"ref": "HEAD", "path": "README.md"}, tc)
	testutil.FailErr(t, "GitShowTool.Run", err)
	if !strings.Contains(showOut, "introspection commit line") {
		t.Fatalf("expected committed line in GitShowTool output: %s", showOut)
	}

	blameTool := &GitBlameTool{Git: gitMgr, Boundary: boundary}
	blameOut, err := blameTool.Run(t.Context(), map[string]any{"path": "README.md"}, tc)
	testutil.FailErr(t, "GitBlameTool.Run", err)
	if !strings.Contains(blameOut, "introspection commit line") {
		t.Fatalf("expected committed line in GitBlameTool output: %s", blameOut)
	}

	writeViewFile("README.md", "dirty before restore\n")
	restoreTool := &GitRestoreTool{Git: gitMgr, Boundary: boundary}
	restoreOut, err := restoreTool.Run(t.Context(), map[string]any{"paths": []any{"README.md"}}, tc)
	testutil.FailErr(t, "GitRestoreTool.Run", err)
	if !strings.Contains(restoreOut, "README.md") {
		t.Fatalf("expected README.md in GitRestoreTool output: %s", restoreOut)
	}
}

func TestWorkerGitRejectsUnbranchedWriteLegAndBlocksNativeGitInternalsWrite(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	tc := nativefixture.Context(dir)
	tc.Identity.WorkerJobID = "worker-unbranched"
	tc.Source.WorkerBranchRoot = ""

	opTool := GitOperationTool{Git: git.NewManager(), Boundary: nativefixture.Boundary(t), Kind: "checkout"}
	_, err := opTool.Run(t.Context(), map[string]any{"branch": "main"}, tc)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_OPERATION_PRECONDITION" {
		t.Fatalf("expected GIT_OPERATION_PRECONDITION for unbranched worker, got %v", err)
	}

	for _, rel := range []string{".git/HEAD", ".git/index", ".git/config", ".git/hooks/pre-commit", ".git/refs/heads/main"} {
		if _, denied, err := tools.CheckNativeGitMetadataPath(rel, true); !denied || !errors.Is(err, privategit.ErrNativeToolGitMutationForbidden) {
			t.Fatalf("expected ErrNativeToolGitMutationForbidden for %s, got denied=%v err=%v", rel, denied, err)
		}
	}
}
