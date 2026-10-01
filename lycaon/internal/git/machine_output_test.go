package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMachineOutputPreservesUnusualPathsAndMixedStatus(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	ctx := context.Background()
	path := " leading\nname\t.txt"
	absPath := filepath.Join(dir, path)
	testutil.FailErr(t, "write initial path", os.WriteFile(absPath, []byte("one\n"), 0o600))
	runTestGit(t, dir, "add", "--", path)
	runTestGit(t, dir, "commit", "-m", "initial")

	testutil.FailErr(t, "write staged path", os.WriteFile(absPath, []byte("two\n"), 0o600))
	runTestGit(t, dir, "add", "--", path)
	testutil.FailErr(t, "write unstaged path", os.WriteFile(absPath, []byte("three\n"), 0o600))

	mgr := NewManager()
	status, err := mgr.Status(ctx, dir)
	testutil.FailErr(t, "read status", err)
	if status.StagedCount != 1 || status.UnstagedCount != 1 {
		t.Fatalf("counts = staged %d unstaged %d, want 1/1", status.StagedCount, status.UnstagedCount)
	}
	if len(status.Files) != 1 || status.Files[0].Path != path || status.Files[0].Status != "MM" {
		t.Fatalf("status files = %#v", status.Files)
	}

	stats, err := mgr.DiffStat(ctx, dir, GitDiffOpts{Paths: []string{path}})
	testutil.FailErr(t, "read diff stat", err)
	if len(stats) != 1 || stats[0].Path != path {
		t.Fatalf("diff stats = %#v", stats)
	}
	touches, err := mgr.LastTouchByPath(ctx, dir)
	testutil.FailErr(t, "read last touch", err)
	if _, ok := touches[path]; !ok {
		t.Fatalf("last touch omitted unusual path: %#v", touches)
	}
}

func TestRepositoryMutationLeaseSpansLinkedWorktrees(t *testing.T) {
	mgr, dir := initWorktreeRepo(t)
	base, err := mgr.Branch(t.Context(), dir)
	testutil.FailErr(t, "read base branch", err)
	linked := filepath.Join(worktreeParent(t), "lease-linked")
	testutil.FailErr(t, "add linked worktree", mgr.AddWorktree(t.Context(), dir, linked, "session/lease", base))

	release, err := gitlease.Repository(t.Context(), dir)
	testutil.FailErr(t, "acquire primary lease", err)
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	secondRelease, err := gitlease.Repository(ctx, linked)
	if secondRelease != nil {
		secondRelease()
	}
	if err == nil {
		t.Fatal("linked worktree acquired a repository lease while primary held it")
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
