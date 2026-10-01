package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGitManagerCommitIgnoresRepoHooksPath(t *testing.T) {
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	gitCfg := func(args ...string) {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitCfg("config", "user.email", "test@example.com")
	gitCfg("config", "user.name", "Test")
	gitCfg("config", "core.hooksPath", ".githooks")

	hooks := filepath.Join(dir, ".githooks")
	testutil.FailErr(t, "mkdir hooks", os.MkdirAll(hooks, 0o755))
	hook := filepath.Join(hooks, "pre-commit")
	sentinel := filepath.Join(dir, "hook-sentinel")
	script := "#!/bin/sh\n" +
		"echo ran > " + sentinel + "\n" +
		"echo 'task: [build] go build ./...'\n" +
		"echo 'pattern ./...: directory prefix . does not contain main module'\n" +
		"echo 'task: Failed to run task \"build\": exit status 1'\n" +
		"exit 1\n"
	testutil.FailErr(t, "write hook", os.WriteFile(hook, []byte(script), 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0o644))

	mgr := git.NewManager()
	_, err := mgr.Commit(context.Background(), dir, git.GitCommitOpts{Message: "should-succeed", Paths: []string{"a.txt"}})
	if err != nil {
		t.Fatalf("commit must succeed with hooks neutralized: %v", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("repo-local hooksPath pre-commit ran")
	}
}

// Explicit author environment values outrank repository identity.
func TestGitManagerCommitKeepsUserIdentity(t *testing.T) {
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	runRef := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	runRef("config", "user.name", "Repo Author")
	runRef("config", "user.email", "repo.author@example.com")

	// Isolate the global config so the repository's identity is the only one set.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")

	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0o644))

	mgr := git.NewManager()
	if _, err := mgr.Commit(context.Background(), dir, git.GitCommitOpts{Message: "identity check", Paths: nil}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	log := exec.CommandContext(t.Context(), "git", "-C", dir, "log", "-1", "--format=%an <%ae>")
	log.Env = lyexec.LocalGitEnv()
	out, err := log.CombinedOutput()
	testutil.FailErr(t, "git log", err)
	if got := strings.TrimSpace(string(out)); got != "Repo Author <repo.author@example.com>" {
		t.Fatalf("author = %q, want the repository's configured identity", got)
	}
}
