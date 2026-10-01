package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func initGitRepo(t *testing.T, dir string) {
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	config := func(key, val string) {
		cmd := exec.CommandContext(t.Context(), "git", "-C", dir, "config", key, val)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v %s", key, err, out)
		}
	}
	config("user.email", "test@example.com")
	config("user.name", "Test")
}

func TestGitManagerShowAndRef(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initGitRepo(t, dir)
	path := filepath.Join(dir, "README.md")
	testutil.FailErr(t, "write README", os.WriteFile(path, []byte("hello\n"), 0o644))
	gitCmd := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitCmd("add", "README.md")
	gitCmd("commit", "-m", "init")

	mgr := NewManager()
	show, err := mgr.Show(ctx, dir, GitShowOpts{Path: "README.md"})
	testutil.FailErr(t, "Show blob", err)
	if show.Kind != "blob" || !strings.Contains(show.Content, "hello") {
		t.Fatalf("show = %#v", show)
	}

	refs, err := mgr.RevParse(ctx, dir, []string{"HEAD"})
	testutil.FailErr(t, "RevParse", err)
	if len(refs) != 1 || refs[0].SHA == "" {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestGitManagerBlameAndLogPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initGitRepo(t, dir)
	path := filepath.Join(dir, "src.go")
	testutil.FailErr(t, "write src", os.WriteFile(path, []byte("package main\n"), 0o644))
	gitCmd := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitCmd("add", "src.go")
	gitCmd("commit", "-m", "add src")

	mgr := NewManager()
	lines, err := mgr.Blame(ctx, dir, GitBlameOpts{Path: "src.go"})
	testutil.FailErr(t, "Blame", err)
	if len(lines) == 0 {
		t.Fatal("expected blame lines")
	}

	commits, err := mgr.Log(ctx, dir, GitLogOpts{Path: "src.go", Limit: 5})
	testutil.FailErr(t, "Log path", err)
	if len(commits) != 1 {
		t.Fatalf("commits = %#v", commits)
	}
}

func TestGitManagerDiffStagedAndRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initGitRepo(t, dir)
	path := filepath.Join(dir, "file.txt")
	testutil.FailErr(t, "write file", os.WriteFile(path, []byte("v1\n"), 0o644))
	gitCmd := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitCmd("add", "file.txt")
	gitCmd("commit", "-m", "init")
	testutil.FailErr(t, "modify file", os.WriteFile(path, []byte("v2\n"), 0o644))
	gitCmd("add", "file.txt")

	mgr := NewManager()
	diff, err := mgr.Diff(ctx, dir, GitDiffOpts{Staged: true})
	testutil.FailErr(t, "Diff staged", err)
	if !strings.Contains(diff, "v2") {
		t.Fatalf("staged diff = %q", diff)
	}

	testutil.FailErr(t, "modify worktree", os.WriteFile(path, []byte("v3\n"), 0o644))
	testutil.FailErr(t, "Restore", mgr.Restore(ctx, dir, GitRestoreOpts{
		Paths: []string{"file.txt"}, Source: "HEAD", Staged: true, Worktree: true,
	}))
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored", err)
	if string(data) != "v1\n" {
		t.Fatalf("restored content = %q", string(data))
	}
}

func TestGitManagerRestoreDefaultsToWorktreeFromIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initGitRepo(t, dir)
	path := filepath.Join(dir, "file.txt")
	testutil.FailErr(t, "write original", os.WriteFile(path, []byte("v1\n"), 0o644))
	gitCmd := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitCmd("add", "file.txt")
	gitCmd("commit", "-m", "init")
	testutil.FailErr(t, "write staged version", os.WriteFile(path, []byte("v2\n"), 0o644))
	gitCmd("add", "file.txt")
	testutil.FailErr(t, "write worktree version", os.WriteFile(path, []byte("v3\n"), 0o644))

	mgr := NewManager()
	testutil.FailErr(t, "restore worktree", mgr.Restore(ctx, dir, GitRestoreOpts{Paths: []string{"file.txt"}}))
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read worktree restore", err)
	if string(data) != "v2\n" {
		t.Fatalf("worktree content = %q, want staged content", data)
	}
	staged, err := mgr.Diff(ctx, dir, GitDiffOpts{Staged: true})
	testutil.FailErr(t, "read staged diff", err)
	if !strings.Contains(staged, "+v2") {
		t.Fatalf("default restore changed the index: %q", staged)
	}
}

func TestGitManagerBranches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initGitRepo(t, dir)
	path := filepath.Join(dir, "README.md")
	testutil.FailErr(t, "write README", os.WriteFile(path, []byte("x\n"), 0o644))
	gitCmd := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitCmd("add", "README.md")
	gitCmd("commit", "-m", "init")
	mgr := NewManager()
	branches, err := mgr.Branches(ctx, dir, 10)
	testutil.FailErr(t, "Branches", err)
	if len(branches) == 0 {
		t.Fatal("expected default branch")
	}
}

func TestGitManagerShowStat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initGitRepo(t, dir)
	path := filepath.Join(dir, "README.md")
	testutil.FailErr(t, "write README", os.WriteFile(path, []byte("hello\n"), 0o644))
	gitCmd := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitCmd("add", "README.md")
	gitCmd("commit", "-m", "init")

	mgr := NewManager()
	show, err := mgr.Show(ctx, dir, GitShowOpts{Stat: true})
	testutil.FailErr(t, "Show stat", err)
	if !show.Stat || !strings.Contains(show.Content, "README.md") {
		t.Fatalf("show = %#v", show)
	}
}

func TestGitManagerStatusIgnored(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initGitRepo(t, dir)
	testutil.FailErr(t, "write gitignore", os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644))
	testutil.FailErr(t, "write ignored", os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("secret\n"), 0o644))
	testutil.FailErr(t, "write tracked", os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("public\n"), 0o644))

	mgr := NewManager()
	entries, err := mgr.StatusIgnored(ctx, dir, []string{"ignored.txt", "tracked.txt"})
	testutil.FailErr(t, "StatusIgnored", err)
	if len(entries) != 1 || entries[0].Path != "ignored.txt" || entries[0].Status != "!!" {
		t.Fatalf("expected 1 ignored entry, got %#v", entries)
	}
}
