package gittest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestInitCommitAndCommitAll(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write first file", os.WriteFile(filepath.Join(dir, "first.txt"), []byte("one"), 0o600))
	gittest.InitCommit(t, dir, "initial")

	testutil.FailErr(t, "write second file", os.WriteFile(filepath.Join(dir, "second.txt"), []byte("two"), 0o600))
	gittest.CommitAll(t, dir, "second")

	if got := strings.TrimSpace(gittest.Run(t, dir, "rev-list", "--count", "HEAD")); got != "2" {
		t.Fatalf("commit count = %q, want 2", got)
	}
}

func TestRunSetsFixtureCommitDate(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "dated.txt"), []byte("dated"), 0o600))
	gittest.Init(t, dir)
	gittest.Run(t, dir, "add", "dated.txt")
	gittest.CommitAt(t, dir, "dated", time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))

	if got := strings.TrimSpace(gittest.Run(t, dir, "show", "-s", "--format=%aI", "HEAD")); got != "2020-01-02T03:04:05Z" {
		t.Fatalf("author date = %q", got)
	}
	if got := strings.TrimSpace(gittest.Run(t, dir, "show", "-s", "--format=%cI", "HEAD")); got != "2020-01-02T03:04:05Z" {
		t.Fatalf("committer date = %q", got)
	}
}
