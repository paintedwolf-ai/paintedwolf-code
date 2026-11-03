package toolcommand

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func TestGitOperationCommandReplacements(t *testing.T) {
	tests := []struct {
		command, tool string
		args          map[string]any
	}{
		{"git log --oneline -6 benchmark-page", "git_log", map[string]any{"ref": "benchmark-page", "limit": 6}},
		{"git diff main dev", "git_diff", map[string]any{"base_ref": "main", "head_ref": "dev"}},
		{"git log main..feature -- src/main.go", "git_log", map[string]any{"ref": "main..feature", "path": "src/main.go"}},
		{"git checkout main", "git_checkout", map[string]any{"branch": "main"}},
		{"git switch -c feature main", "git_checkout", map[string]any{"branch": "feature", "create": true, "ref": "main"}},
		{"git merge --ff-only feature", "git_merge", map[string]any{"ref": "feature", "mode": "ff_only"}},
		{"git merge --abort", "git_merge", map[string]any{"action": "abort"}},
		{"git merge --no-commit --no-ff main", "git_merge", map[string]any{"ref": "main", "mode": "no_ff", "commit": false}},
		{"git checkout -- AGENTS.md", "git_restore", map[string]any{"paths": []any{"AGENTS.md"}}},
		{"git checkout HEAD -- AGENTS.md docs/AGENTS.md", "git_restore", map[string]any{"paths": []any{"AGENTS.md", "docs/AGENTS.md"}, "source": "HEAD", "staged": true, "worktree": true}},
		{"git stash push -u -m 'Save work' -- 'file name.txt'", "git_stash", map[string]any{"action": "save", "include_untracked": true, "message": "Save work", "paths": []any{"file name.txt"}}},
		{"git stash apply --index stash@{1}", "git_stash", map[string]any{"action": "apply", "reinstate_index": true, "ref": "stash@{1}"}},
		{"git stash list", "git_stash_list", map[string]any{}},
		{"git log --all", "git_log", map[string]any{"all": true}},
		{"git revert HEAD", "git_revert", map[string]any{"ref": "HEAD"}},
		{"git worktree list", "git_worktree", map[string]any{"action": "list"}},
		{"git submodule status", "git_submodule", map[string]any{"action": "status"}},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got, ok := ExactCommandReplacement(t.Context(), tt.command, t.TempDir(), "")
			if !ok || len(got) != 1 || got[0].Tool != tt.tool || !reflect.DeepEqual(got[0].Args, tt.args) {
				t.Fatalf("replacement=%+v matched=%v", got, ok)
			}
		})
	}
	for _, command := range []string{"git checkout --", "git checkout HEAD -- '*.md'", "git checkout main dev -- file.txt", "git checkout -f -- file.txt", "git restore -- '*.md'", "git checkout -f main", "git switch -C main", "git merge --squash main", "git stash push", "git stash pop", "git stash clear", "git stash push -- .//", "git stash push -- '*.md'", "git log -- '*.md'", "git log --format=%B", "git log -1000"} {
		t.Run(command, func(t *testing.T) {
			if calls, ok := ExactCommandReplacement(t.Context(), command, t.TempDir(), ""); ok {
				t.Fatalf("inexact command redirected: %+v", calls)
			}
		})
	}
}

func TestPrivateGitCommandOperationsAndReplacementsAcrossAdapters(t *testing.T) {
	for _, adapter := range []string{"fuselinux", "nfsdarwin", "eagercopy"} {
		t.Run(adapter, func(t *testing.T) {
			verifyPrivateGitCommandExecution(t, adapter)
		})
	}
}

func verifyPrivateGitCommandExecution(t *testing.T, adapter string) {
	trunkDir := t.TempDir()
	gittest.Init(t, trunkDir)
	gittest.Run(t, trunkDir, "config", "user.name", "Test")
	gittest.Run(t, trunkDir, "config", "user.email", "test@example.com")
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(trunkDir, "app.txt"), []byte("v1\n"), 0o644))
	gittest.Run(t, trunkDir, "add", "--", "app.txt")
	gittest.Run(t, trunkDir, "commit", "-m", "initial")
	gittest.Run(t, trunkDir, "branch", "-M", "main")

	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(t.TempDir(), "pgit")})
	testutil.FailErr(t, "new privategit manager", err)
	viewID := "cmd-" + adapter
	t.Cleanup(func() {
		_ = mgr.ReleaseView(viewID)
		_ = mgr.Store().Close()
	})

	var wtDir string
	if adapter == "eagercopy" {
		wtDir = filepath.Join(t.TempDir(), "materialized-wt")
		_, err = mgr.InitializeMaterializedSeed(t.Context(), privategit.MaterializedSeedConfig{
			ViewID: viewID, WorkerID: "worker-cmd", TrunkDir: trunkDir, TargetDir: wtDir,
		})
		testutil.FailErr(t, "init materialized seed", err)
	} else {
		core, cErr := layeredfs.New(layeredfs.Config{
			ViewID: viewID, BackingDir: filepath.Join(t.TempDir(), "core-"+adapter), GitInterceptor: mgr,
			Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		})
		testutil.FailErr(t, "layeredfs.New", cErr)
		t.Cleanup(func() { _ = core.ReleaseView() })
		testutil.FailErr(t, "MountedClaim", mgr.MountedClaim(viewID, "worker-cmd", "primary", trunkDir, core))
		var finish func() error
		wtDir, finish, _, err = privategit.PrepareManagerViewOperation(t.Context(), mgr, trunkDir, "worker-cmd")
		testutil.FailErr(t, "PrepareManagerViewOperation", err)
		_ = finish()
	}

	testutil.FailErr(t, "write worker file", os.WriteFile(filepath.Join(wtDir, "worker_only.txt"), []byte("only in view\n"), 0o644))
	calls, ok := ExactCommandReplacement(t.Context(), "git diff worker_only.txt", trunkDir, "")
	if !ok || len(calls) != 1 || calls[0].Tool != "git_diff" || !reflect.DeepEqual(calls[0].Args["paths"], []any{"worker_only.txt"}) {
		t.Fatalf("expected git diff replacement to recognize private view path, got %+v (ok=%v)", calls, ok)
	}

	exec := privategit.NewGitExecutor(mgr, viewID, privategit.RemotePolicy{})
	testutil.FailErr(t, "modify tracked file", os.WriteFile(filepath.Join(wtDir, "app.txt"), []byte("v2\n"), 0o644))

	for _, cmd := range []string{
		"git status --short",
		"git diff",
		"git checkout -b cmd-branch",
		"git commit -a -m 'cmd commit'",
		"git log --oneline -2",
		"git revert HEAD",
		"git switch main",
		"git switch --create cmd-branch-2",
		"git worktree list",
		"git submodule status",
	} {
		out, cmdErr := ExecutePrivateGitCommand(t.Context(), exec, cmd)
		testutil.FailErr(t, cmd, cmdErr)
		if cmd == "git log --oneline -2" && !strings.Contains(out, "cmd commit") {
			t.Fatalf("expected cmd commit in log output: %s", out)
		}
	}
}

