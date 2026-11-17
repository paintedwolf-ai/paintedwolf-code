package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/testutil"
)

func landFixture(t *testing.T) (*Manager, WorktreeLandRequest) {
	t.Helper()
	mgr, dir := initWorktreeRepo(t)
	base, err := mgr.Branch(t.Context(), dir)
	testutil.FailErr(t, "base branch", err)
	req := WorktreeLandRequest{
		BaseDir: dir, BaseBranch: base,
		WorktreePath: filepath.Join(worktreeParent(t), "topic"), Branch: "session/topic",
	}
	testutil.FailErr(t, "create worktree", mgr.AddWorktree(t.Context(), dir, req.WorktreePath, req.Branch, base))
	testutil.FailErr(t, "write topic", os.WriteFile(filepath.Join(req.WorktreePath, "topic.txt"), []byte("topic\n"), 0o600))
	_, commitErr := mgr.Commit(t.Context(), req.WorktreePath, GitCommitOpts{Message: "topic", Paths: []string{"topic.txt"}})
	testutil.FailErr(t, "commit topic", commitErr)
	return mgr, req
}

func TestLandWorktreeRevalidatesUnderRepositoryLease(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"base_branch", "source_branch", "base_dirty", "worktree_dirty"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			mgr, req := landFixture(t)
			before, err := ReadHeadSHA(req.BaseDir)
			testutil.FailErr(t, "base head before", err)
			release, err := gitlease.Repository(t.Context(), req.BaseDir)
			testutil.FailErr(t, "hold competing mutation lease", err)
			defer release()
			result := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				_, err := mgr.LandWorktree(t.Context(), req)
				result <- err
			}()
			<-started
			want := change
			switch change {
			case "base_branch", "source_branch":
				dir := req.BaseDir
				want = "base_on_other_branch"
				if change == "source_branch" {
					dir, want = req.WorktreePath, "worktree_stale"
				}
				out, code, err := gitexec.Run(t.Context(), dir, []string{"checkout", "-b", "intervening"}, hermeticOpts(0))
				testutil.FailErr(t, "competing checkout", err)
				if code != 0 {
					t.Fatalf("competing checkout: %s", out)
				}
			case "base_dirty", "worktree_dirty":
				dir := req.BaseDir
				if change == "worktree_dirty" {
					dir = req.WorktreePath
				}
				testutil.FailErr(t, "competing write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("unsaved\n"), 0o600))
			}
			release()
			err = <-result
			var blocked *WorktreeLandError
			if !errors.As(err, &blocked) || blocked.Reason != want {
				t.Fatalf("land error = %v, want typed %s", err, want)
			}
			after, err := ReadHeadSHA(req.BaseDir)
			testutil.FailErr(t, "base head after", err)
			if before != after {
				t.Fatalf("refused land moved base from %s to %s", before, after)
			}
		})
	}
}

func TestLandWorktreeCountsExactSourceAndIsIdempotent(t *testing.T) {
	t.Parallel()
	mgr, req := landFixture(t)
	count, err := mgr.LandWorktree(t.Context(), req)
	testutil.FailErr(t, "first land", err)
	if count != 1 {
		t.Fatalf("first land count = %d", count)
	}
	before, err := ReadHeadSHA(req.BaseDir)
	testutil.FailErr(t, "landed head", err)
	count, err = mgr.LandWorktree(t.Context(), req)
	testutil.FailErr(t, "second land", err)
	after, err := ReadHeadSHA(req.BaseDir)
	testutil.FailErr(t, "head after second land", err)
	if count != 0 || before != after {
		t.Fatalf("second land count=%d head changed=%v", count, before != after)
	}
}

// Each state that blocks a land refuses with its reason before anything merges,
// and the base keeps its branch tip.
func TestLandWorktreeRefusesEachBlockedState(t *testing.T) {
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		out, code, err := gitexec.Run(t.Context(), dir, args, hermeticOpts(0))
		if err != nil || code != 0 {
			t.Fatalf("git %v: exit %d: %v\n%s", args, code, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, tc := range []struct {
		reason, currentBranch string
		block                 func(*testing.T, WorktreeLandRequest)
	}{
		{"worktree_stale", "", func(t *testing.T, req WorktreeLandRequest) {
			testutil.FailErr(t, "remove worktree", os.RemoveAll(req.WorktreePath))
		}},
		{"worktree_dirty", "", func(t *testing.T, req WorktreeLandRequest) {
			testutil.FailErr(t, "dirty worktree", os.WriteFile(filepath.Join(req.WorktreePath, "dirty.txt"), []byte("d\n"), 0o600))
		}},
		{"base_branch_missing", "", func(t *testing.T, req WorktreeLandRequest) {
			run(t, req.BaseDir, "checkout", "-q", "-b", "other")
			run(t, req.BaseDir, "branch", "-q", "-D", req.BaseBranch)
		}},
		{"base_on_other_branch", "other", func(t *testing.T, req WorktreeLandRequest) {
			run(t, req.BaseDir, "checkout", "-q", "-b", "other")
		}},
		{"base_dirty", "", func(t *testing.T, req WorktreeLandRequest) {
			testutil.FailErr(t, "dirty base", os.WriteFile(filepath.Join(req.BaseDir, "base-dirty.txt"), []byte("d\n"), 0o600))
		}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			mgr, req := landFixture(t)
			before := run(t, req.BaseDir, "rev-parse", "HEAD")
			tc.block(t, req)
			_, err := mgr.LandWorktree(t.Context(), req)
			var blocked *WorktreeLandError
			if !errors.As(err, &blocked) || blocked.Reason != tc.reason || blocked.CurrentBranch != tc.currentBranch {
				t.Fatalf("land = %v, want blocked by %s (current branch %q)", err, tc.reason, tc.currentBranch)
			}
			if after := run(t, req.BaseDir, "rev-parse", "HEAD"); after != before {
				t.Fatalf("a blocked land moved the base from %s to %s", before, after)
			}
		})
	}
}
