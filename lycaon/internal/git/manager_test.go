package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGitManagerCommitAndStatus(t *testing.T) {
	tmpDir := t.TempDir()
	// Remove inherited repository variables before creating the scratch repository.
	initCmd := exec.CommandContext(t.Context(), "git", "init", tmpDir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	mgr := NewManager()
	if _, err := mgr.Commit(context.Background(), tmpDir, GitCommitOpts{Message: "Initial commit", Paths: []string{"test.txt"}}); err != nil {
		testutil.FailErr(t, "mgr.Commit failed", err)
	}
	status, err := mgr.Status(context.Background(), tmpDir)
	testutil.FailErr(t, "mgr.Status failed", err)
	if status.Dirty {
		t.Fatalf("expected clean status: %+v", status)
	}
	if len(status.RecentCommits) != 0 {
		t.Fatalf("Status must not attach RecentCommits: %#v", status.RecentCommits)
	}
	commits, err := mgr.Log(context.Background(), tmpDir, GitLogOpts{Limit: 5})
	testutil.FailErr(t, "mgr.Log failed", err)
	if len(commits) != 1 || commits[0].Subject != "Initial commit" {
		t.Fatalf("Log subjects = %#v", commits)
	}
}

// TestGitManagerChangedPathsPorcelain covers leading-space status codes.
func TestGitManagerChangedPathsPorcelain(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	mgr := NewManager()
	writeFile := func(rel, content string) {
		testutil.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644))
	}
	git := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")

	// README.md is committed then modified-not-staged → ` M README.md`.
	writeFile("README.md", "base\n")
	git("add", "README.md")
	git("commit", "-m", "init")
	writeFile("README.md", "changed\n")
	// staged.go is new and staged → `A  staged.go`.
	writeFile("staged.go", "package x\n")
	git("add", "staged.go")
	// untracked.txt is brand new and unstaged → `?? untracked.txt`.
	writeFile("untracked.txt", "x\n")

	paths, err := mgr.ChangedPaths(ctx, dir)
	testutil.FailErr(t, "ChangedPaths", err)
	status, err := mgr.Status(ctx, dir)
	testutil.FailErr(t, "Status", err)

	got := map[string]bool{}
	for _, p := range paths {
		got[p] = true
	}
	for _, want := range []string{"README.md", "staged.go", "untracked.txt"} {
		if !got[want] {
			t.Fatalf("ChangedPaths = %v, missing %q (porcelain slice corruption?)", paths, want)
		}
	}
	if len(paths) != 3 {
		t.Fatalf("ChangedPaths = %v, want exactly 3 paths", paths)
	}
	if !status.Dirty || status.UnstagedCount != 2 {
		t.Fatalf("Status = %+v, want two unstaged paths including untracked.txt", status)
	}
	statusPaths := map[string]bool{}
	for _, entry := range status.Files {
		statusPaths[entry.Path] = true
	}
	if !statusPaths["untracked.txt"] {
		t.Fatalf("Status files = %+v, missing untracked.txt", status.Files)
	}
}

// Untracked directories are listed one entry per file, never collapsed to
// `dir/`: staging, paging, and invalidation all address explicit file paths.
func TestGitManagerStatusExpandsUntrackedDirectories(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	testutil.FailErr(t, "mkdir fixtures", os.MkdirAll(filepath.Join(dir, "fixtures", "nested"), 0o755))
	for _, rel := range []string{"fixtures/a.txt", "fixtures/b.txt", "fixtures/nested/c.txt"} {
		testutil.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte("x\n"), 0o644))
	}

	mgr := NewManager()
	status, err := mgr.Status(ctx, dir)
	testutil.FailErr(t, "Status", err)
	got := map[string]string{}
	for _, entry := range status.Files {
		got[entry.Path] = entry.Status
	}
	for _, want := range []string{"fixtures/a.txt", "fixtures/b.txt", "fixtures/nested/c.txt"} {
		if got[want] != "??" {
			t.Fatalf("Status files = %+v, want %q listed as untracked", status.Files, want)
		}
	}
	if _, collapsed := got["fixtures/"]; collapsed || len(status.Files) != 3 {
		t.Fatalf("Status files = %+v, want three file entries and no collapsed directory", status.Files)
	}
	paths, err := mgr.ChangedPaths(ctx, dir)
	testutil.FailErr(t, "ChangedPaths", err)
	if len(paths) != 3 {
		t.Fatalf("ChangedPaths = %v, want three file paths", paths)
	}
}
