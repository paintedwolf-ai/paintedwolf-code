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

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Clear inherited Git variables so fixture commands target the scratch repository.
		cmd.Env = lyexec.LocalGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644))
	run("add", "a.txt")
	run("commit", "-m", "init")
}

func TestFastRead_HeadSHAAndBranchParity(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	fastSHA, err := git.ReadHeadSHA(dir)
	testutil.FailErr(t, "ReadHeadSHA", err)
	fastBranch, err := git.ReadBranch(dir)
	testutil.FailErr(t, "ReadBranch", err)

	m := git.NewManager()
	ctx := context.Background()
	execSHA, err := m.HeadSHA(ctx, dir)
	testutil.FailErr(t, "Manager.HeadSHA", err)
	execBranch, err := m.Branch(ctx, dir)
	testutil.FailErr(t, "Manager.Branch", err)

	if fastSHA != execSHA {
		t.Fatalf("HeadSHA mismatch fast=%q exec=%q", fastSHA, execSHA)
	}
	if fastBranch != execBranch || fastBranch != "main" {
		t.Fatalf("Branch mismatch fast=%q exec=%q", fastBranch, execBranch)
	}

	// Direct rev-parse for independence from Manager fast-path.
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.Output()
	testutil.FailErr(t, "rev-parse", err)
	want := strings.TrimSpace(string(out))
	if fastSHA != want {
		t.Fatalf("fast %q != rev-parse %q", fastSHA, want)
	}
}

func TestFastRead_AheadBehindEqualTips(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	// Simulate upstream pointing at same tip via a fake remote-tracking ref.
	gitDir := filepath.Join(dir, ".git")
	testutil.FailErr(t, "mkdir remotes", os.MkdirAll(filepath.Join(gitDir, "refs", "remotes", "origin"), 0o755))
	sha, err := git.ReadHeadSHA(dir)
	testutil.FailErr(t, "sha", err)
	testutil.FailErr(t, "write upstream", os.WriteFile(filepath.Join(gitDir, "refs", "remotes", "origin", "main"), []byte(sha+"\n"), 0o644))
	cfg := "[branch \"main\"]\n\tremote = origin\n\tmerge = refs/heads/main\n"
	testutil.FailErr(t, "append config", os.WriteFile(filepath.Join(gitDir, "config"),
		append(mustRead(t, filepath.Join(gitDir, "config")), []byte(cfg)...), 0o644))

	ab, err := git.ReadAheadBehind(dir)
	testutil.FailErr(t, "ReadAheadBehind", err)
	if !ab.Exact || ab.Ahead != 0 || ab.Behind != 0 {
		t.Fatalf("expected Exact 0/0, got %#v", ab)
	}
	m := git.NewManager()
	ahead, behind, _, err := m.AheadBehindCounts(context.Background(), dir)
	testutil.FailErr(t, "AheadBehindCounts", err)
	if ahead != 0 || behind != 0 {
		t.Fatalf("counts %d/%d", ahead, behind)
	}
}

// linkedWorktree creates separate HEAD storage with shared refs and config.
func linkedWorktree(t *testing.T, branch string) (main, worktree string) {
	t.Helper()
	main = t.TempDir()
	initGitRepo(t, main)
	worktree = filepath.Join(t.TempDir(), "wt")

	m := git.NewManager()
	testutil.FailErr(t, "AddWorktree", m.AddWorktree(context.Background(), main, worktree, branch, "main"))
	return main, worktree
}

// A linked worktree keeps HEAD beside itself and its branch tip in the shared directory.
func TestFastRead_HeadSHAInLinkedWorktree(t *testing.T) {
	main, worktree := linkedWorktree(t, "feature")

	sha, err := git.ReadHeadSHA(worktree)
	testutil.FailErr(t, "ReadHeadSHA in a linked worktree", err)

	mainSHA, err := git.ReadHeadSHA(main)
	testutil.FailErr(t, "ReadHeadSHA in the main worktree", err)
	if sha != mainSHA {
		t.Fatalf("worktree HEAD = %q, want the base tip %q", sha, mainSHA)
	}

	branch, err := git.ReadBranch(worktree)
	testutil.FailErr(t, "ReadBranch in a linked worktree", err)
	if branch != "feature" {
		t.Fatalf("worktree branch = %q, want feature", branch)
	}
	// HEAD is per-worktree: the main tree must still report its own.
	mainBranch, err := git.ReadBranch(main)
	testutil.FailErr(t, "ReadBranch in the main worktree", err)
	if mainBranch != "main" {
		t.Fatalf("main branch = %q, want main", mainBranch)
	}

	// A commit in the linked worktree moves only its HEAD.
	m := git.NewManager()
	ctx := context.Background()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(worktree, "b.txt"), []byte("b"), 0o644))
	_, commitErr1 := m.Commit(ctx, worktree, git.GitCommitOpts{Message: "worktree commit", Paths: []string{"b.txt"}})
	testutil.FailErr(t, "commit", commitErr1)

	moved, err := git.ReadHeadSHA(worktree)
	testutil.FailErr(t, "ReadHeadSHA after commit", err)
	if moved == sha {
		t.Fatal("worktree HEAD did not move after a commit in it")
	}
	execSHA, err := m.HeadSHA(ctx, worktree)
	testutil.FailErr(t, "Manager.HeadSHA", err)
	if moved != execSHA {
		t.Fatalf("fast HEAD %q != engine HEAD %q", moved, execSHA)
	}
	if after, err := git.ReadHeadSHA(main); err != nil || after != sha {
		t.Fatalf("main HEAD = %q (err %v), want it unmoved at %q", after, err, sha)
	}
}

// The upstream a branch names lives in the shared config; a per-worktree git dir has none.
func TestFastRead_AheadBehindInLinkedWorktree(t *testing.T) {
	main, worktree := linkedWorktree(t, "feature")

	// A remote-tracking tip in the shared refs, and an upstream in the shared config.
	commonDir := filepath.Join(main, ".git")
	testutil.FailErr(t, "mkdir remotes", os.MkdirAll(filepath.Join(commonDir, "refs", "remotes", "origin"), 0o755))
	base, err := git.ReadHeadSHA(worktree)
	testutil.FailErr(t, "base sha", err)
	testutil.FailErr(t, "write upstream ref", os.WriteFile(
		filepath.Join(commonDir, "refs", "remotes", "origin", "main"), []byte(base+"\n"), 0o644))
	cfg := "[branch \"feature\"]\n\tremote = origin\n\tmerge = refs/heads/main\n"
	cfgPath := filepath.Join(commonDir, "config")
	testutil.FailErr(t, "append config", os.WriteFile(cfgPath, append(mustRead(t, cfgPath), []byte(cfg)...), 0o644))

	// Equal tips: the fast path answers zero.
	ab, err := git.ReadAheadBehind(worktree)
	testutil.FailErr(t, "ReadAheadBehind at equal tips", err)
	if !ab.Exact || ab.Ahead != 0 || ab.Behind != 0 {
		t.Fatalf("equal tips = %#v, want Exact 0/0", ab)
	}
	if ab.Upstream != "refs/remotes/origin/main" {
		t.Fatalf("upstream = %q, want refs/remotes/origin/main (read from the shared config)", ab.Upstream)
	}

	// Diverged tips require an engine count.
	m := git.NewManager()
	ctx := context.Background()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(worktree, "c.txt"), []byte("c"), 0o644))
	_, commitErr2 := m.Commit(ctx, worktree, git.GitCommitOpts{Message: "ahead by one", Paths: []string{"c.txt"}})
	testutil.FailErr(t, "commit", commitErr2)

	ab, err = git.ReadAheadBehind(worktree)
	testutil.FailErr(t, "ReadAheadBehind after diverging", err)
	if ab.Exact {
		t.Fatalf("diverged worktree reported in-sync: %#v", ab)
	}
	if ab.Upstream != "refs/remotes/origin/main" {
		t.Fatalf("upstream = %q, want refs/remotes/origin/main", ab.Upstream)
	}

	ahead, behind, upstream, err := m.AheadBehindCounts(ctx, worktree)
	testutil.FailErr(t, "AheadBehindCounts", err)
	if ahead != 1 || behind != 0 {
		t.Fatalf("counts = %d/%d, want 1/0", ahead, behind)
	}
	if upstream != "refs/remotes/origin/main" {
		t.Fatalf("counts upstream = %q", upstream)
	}
}

func TestFastRead_LinkedWorktreeResolvesPackedRefs(t *testing.T) {
	main, worktree := linkedWorktree(t, "feature")

	loose := filepath.Join(main, ".git", "refs", "heads", "feature")
	sha := strings.TrimSpace(string(mustRead(t, loose)))
	testutil.FailErr(t, "remove loose ref", os.Remove(loose))
	testutil.FailErr(t, "write packed-refs", os.WriteFile(
		filepath.Join(main, ".git", "packed-refs"),
		[]byte("# pack-refs with: peeled fully-peeled sorted \n"+sha+" refs/heads/feature\n"), 0o644))

	got, err := git.ReadHeadSHA(worktree)
	testutil.FailErr(t, "ReadHeadSHA against packed refs", err)
	if got != sha {
		t.Fatalf("packed ref resolved to %q, want %q", got, sha)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	return b
}
