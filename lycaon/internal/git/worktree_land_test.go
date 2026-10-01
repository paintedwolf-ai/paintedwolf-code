package git

import (
	"errors"
	"os"
	"path/filepath"
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
	for _, change := range []string{"base_branch", "source_branch", "base_dirty", "worktree_dirty"} {
		t.Run(change, func(t *testing.T) {
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
