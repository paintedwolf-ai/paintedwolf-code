package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func initScratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	return dir
}

// The path separator keeps dash-prefixed filenames out of option parsing.
func TestCommitStagesDashPrefixedPathAsLiteral(t *testing.T) {
	t.Parallel()
	dir := initScratchRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "-dash.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	mgr := NewManager()
	if _, err := mgr.Commit(context.Background(), dir, GitCommitOpts{Message: "add literal dash path", Paths: []string{"-dash.txt"}}); err != nil {
		testutil.FailErr(t, "Commit with dash-prefixed path", err)
	}
	status, err := mgr.Status(context.Background(), dir)
	testutil.FailErr(t, "Status", err)
	if status.Dirty {
		t.Fatalf("expected clean status after committing -dash.txt: %+v", status)
	}
}

func TestDiffStatRejectsFlagLikeRef(t *testing.T) {
	t.Parallel()
	dir := initScratchRepo(t)
	mgr := NewManager()
	if _, err := mgr.DiffStat(context.Background(), dir, GitDiffOpts{BaseRef: "--output=/tmp/pwned"}); err == nil {
		t.Fatal("DiffStat(--output=…) = nil, want error")
	}
	if _, err := os.Stat("/tmp/pwned"); err == nil {
		t.Fatal("--output ref was executed: /tmp/pwned exists")
	}
	if _, err := mgr.DiffStat(context.Background(), dir, GitDiffOpts{}); err != nil {
		t.Fatalf("DiffStat(empty ref) = %v, want nil", err)
	}
}

func TestValidateGitRefRejectsFlagLike(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"", "   ", "-f", "--orphan", "-B"} {
		if err := validateGitRef(v, "branch"); err == nil {
			t.Fatalf("validateGitRef(%q) = nil, want error", v)
		}
	}
	for _, v := range []string{"main", "feature/x", "release-1.2", "HEAD~1"} {
		if err := validateGitRef(v, "branch"); err != nil {
			t.Fatalf("validateGitRef(%q) = %v, want nil", v, err)
		}
	}
}

func TestCloneRejectsExtTransportBeforeExec(t *testing.T) {
	t.Parallel()
	mgr := NewManager()
	err := mgr.Clone(context.Background(), "ext::sh -c 'echo pwned'", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsafe git transport") {
		t.Fatalf("Clone(ext::) = %v, want unsafe-transport error", err)
	}
}

func TestCheckoutRejectsFlagLikeBranch(t *testing.T) {
	t.Parallel()
	mgr := NewManager()
	if err := mgr.Checkout(context.Background(), t.TempDir(), "--orphan"); err == nil {
		t.Fatal("Checkout(--orphan) = nil, want error")
	}
	if err := mgr.CreateBranch(context.Background(), t.TempDir(), "-B"); err == nil {
		t.Fatal("CreateBranch(-B) = nil, want error")
	}
}
