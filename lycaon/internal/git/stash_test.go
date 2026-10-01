package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGitManagerInitCreatesRepo(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager()

	if _, err := mgr.Status(context.Background(), dir); err == nil || !errors.Is(err, ErrNotRepository) {
		t.Fatalf("expected non-repo before init, got %v", err)
	}

	testutil.FailErr(t, "git init", mgr.Init(context.Background(), dir))

	status, err := mgr.Status(context.Background(), dir)
	testutil.FailErr(t, "Status after init", err)
	if status == nil {
		t.Fatalf("expected status after init")
	}

	testutil.FailErr(t, "Init idempotent", mgr.Init(context.Background(), dir))
}

func TestGitManagerCheckoutSwitchesBranch(t *testing.T) {
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644))
	run("add", "README.md")
	run("commit", "-m", "init")

	mgr := NewManager()
	base, err := mgr.Branch(context.Background(), dir)
	testutil.FailErr(t, "Branch", err)

	// CreateBranch (checkout -b) makes and switches to a new branch.
	testutil.FailErr(t, "CreateBranch", mgr.CreateBranch(context.Background(), dir, "feat/x"))
	cur, err := mgr.Branch(context.Background(), dir)
	testutil.FailErr(t, "Branch after create", err)
	if cur != "feat/x" {
		t.Fatalf("expected feat/x after create, got %q", cur)
	}

	// Checkout switches back to the base branch.
	testutil.FailErr(t, "Checkout", mgr.Checkout(context.Background(), dir, base))
	cur, err = mgr.Branch(context.Background(), dir)
	testutil.FailErr(t, "Branch after checkout", err)
	if cur != base {
		t.Fatalf("expected %q after checkout, got %q", base, cur)
	}

	branches, err := mgr.Branches(context.Background(), dir, 10)
	testutil.FailErr(t, "Branches", err)
	if len(branches) < 2 {
		t.Fatalf("expected >=2 branches, got %#v", branches)
	}
}

// gitx runs git with the sandbox env, optionally inside dir (empty dir = no -C).
func gitx(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(context.Background(), "git", full...)
	cmd.Env = lyexec.LocalGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestGitManagerDiscardAllRevertsTree(t *testing.T) {
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	gitx(t, dir, "config", "user.email", "test@example.com")
	gitx(t, dir, "config", "user.name", "Test")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644))
	gitx(t, dir, "add", "README.md")
	gitx(t, dir, "commit", "-m", "init")

	testutil.FailErr(t, "modify", os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644))
	testutil.FailErr(t, "untracked", os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o644))

	mgr := NewManager()
	testutil.FailErr(t, "DiscardAll", mgr.DiscardAll(context.Background(), dir))

	status, err := mgr.Status(context.Background(), dir)
	testutil.FailErr(t, "Status", err)
	if status.Dirty {
		t.Fatalf("expected clean tree after discard, got %#v", status.Files)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(got) != "hi\n" {
		t.Fatalf("README not reverted: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file should be removed (err=%v)", err)
	}
}

func TestGitManagerPushPullAheadBehind(t *testing.T) {
	bare := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", "--bare", bare)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init --bare: %v %s", err, out)
	}
	mgr := NewManager()
	ctx := context.Background()

	a := filepath.Join(t.TempDir(), "a")
	gitx(t, "", "clone", bare, a)
	gitx(t, a, "config", "user.email", "a@example.com")
	gitx(t, a, "config", "user.name", "A")
	testutil.FailErr(t, "write a", os.WriteFile(filepath.Join(a, "f.txt"), []byte("1\n"), 0o644))
	gitx(t, a, "add", "f.txt")
	gitx(t, a, "commit", "-m", "c1")
	gitx(t, a, "push", "-u", "origin", "HEAD")

	// A local commit puts the branch one ahead of its upstream.
	testutil.FailErr(t, "edit a", os.WriteFile(filepath.Join(a, "f.txt"), []byte("2\n"), 0o644))
	gitx(t, a, "commit", "-am", "c2")
	st, err := mgr.Status(ctx, a)
	testutil.FailErr(t, "status ahead", err)
	if st.Ahead != 1 || st.Upstream == "" {
		t.Fatalf("expected ahead 1 with upstream, got %#v", st)
	}

	// Push clears the ahead count.
	testutil.FailErr(t, "Push", mgr.Push(ctx, a))
	st, err = mgr.Status(ctx, a)
	testutil.FailErr(t, "status after push", err)
	if st.Ahead != 0 {
		t.Fatalf("expected ahead 0 after push, got %d", st.Ahead)
	}

	// A second clone advances the remote; after fetch, A reads one behind, Pull clears it.
	b := filepath.Join(t.TempDir(), "b")
	gitx(t, "", "clone", bare, b)
	gitx(t, b, "config", "user.email", "b@example.com")
	gitx(t, b, "config", "user.name", "B")
	testutil.FailErr(t, "edit b", os.WriteFile(filepath.Join(b, "f.txt"), []byte("3\n"), 0o644))
	gitx(t, b, "commit", "-am", "c3")
	gitx(t, b, "push", "origin", "HEAD")

	gitx(t, a, "fetch")
	st, err = mgr.Status(ctx, a)
	testutil.FailErr(t, "status behind", err)
	if st.Behind != 1 {
		t.Fatalf("expected behind 1 after fetch, got %d", st.Behind)
	}
	testutil.FailErr(t, "Pull", mgr.Pull(ctx, a))
	st, err = mgr.Status(ctx, a)
	testutil.FailErr(t, "status after pull", err)
	if st.Behind != 0 {
		t.Fatalf("expected behind 0 after pull, got %d", st.Behind)
	}
}

func TestGitManagerStashClearsWorkingTree(t *testing.T) {
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	git := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	testutil.FailErr(t, "write tracked", os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))
	git("add", "README.md")
	git("commit", "-m", "init")

	// Dirty the tree: modify the tracked file and add an untracked one.
	testutil.FailErr(t, "modify", os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644))
	testutil.FailErr(t, "write untracked", os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644))

	mgr := NewManager()
	dirty, err := mgr.Status(context.Background(), dir)
	testutil.FailErr(t, "Status before", err)
	if !dirty.Dirty {
		t.Fatalf("expected dirty tree before stash")
	}

	testutil.FailErr(t, "Stash", mgr.Stash(context.Background(), dir, "wip"))

	clean, err := mgr.Status(context.Background(), dir)
	testutil.FailErr(t, "Status after", err)
	if clean.Dirty {
		t.Fatalf("expected clean tree after stash, got %#v", clean.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file should be stashed (err=%v)", err)
	}
}
