package indexwatch_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func policyOnly(abs string) bool { return strings.EqualFold(filepath.Base(abs), "AGENTS.md") }

// branchRepo has main with docs/AGENTS.md "main" and branch other with "other".
func branchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gittest.Init(t, dir)
	write(t, dir, "docs/AGENTS.md", "main\n")
	write(t, dir, "README.md", "readme\n")
	gittest.CommitAll(t, dir, "Initial")
	gittest.Run(t, dir, "branch", "-M", "main")
	gittest.Run(t, dir, "checkout", "-q", "-b", "other")
	write(t, dir, "docs/AGENTS.md", "other\n")
	write(t, dir, "README.md", "other readme\n")
	gittest.CommitAll(t, dir, "Other")
	gittest.Run(t, dir, "checkout", "-q", "main")
	return dir
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644))
}

// lockDir makes a directory's entries unreplaceable, the way the sandbox
// refuses the worktree write while Git still updates its index.
func lockDir(t *testing.T, dir string) {
	t.Helper()
	testutil.FailErr(t, "lock directory", os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func TestBlockedCheckoutLeavesPolicyFileBehindTheIndex(t *testing.T) {
	dir := branchRepo(t)
	snapshot := indexwatch.Take(dir)
	defer snapshot.Release()
	if !snapshot.Active() {
		t.Fatal("snapshot inactive in a repository")
	}
	lockDir(t, filepath.Join(dir, "docs"))
	// Git reports the unlink failure and still exits 0 with the branch switched.
	gittest.Run(t, dir, "checkout", "-q", "other")

	result, err := snapshot.Stale(t.Context(), policyOnly)
	testutil.FailErr(t, "compare index", err)
	if !slices.Equal(result.Stale, []string{"docs/AGENTS.md"}) {
		t.Fatalf("stale = %v, want [docs/AGENTS.md]", result.Stale)
	}
	if len(result.Leftover) != 0 || len(result.Conflicted) != 0 {
		t.Fatalf("unexpected leftovers %v or conflicts %v", result.Leftover, result.Conflicted)
	}
}

func TestUnchangedIndexObservesNothingWithoutRunningGit(t *testing.T) {
	dir := branchRepo(t)
	snapshot := indexwatch.Take(dir)
	defer snapshot.Release()
	result, err := snapshot.Stale(t.Context(), func(string) bool {
		t.Fatal("an unchanged index must not be compared")
		return false
	})
	testutil.FailErr(t, "compare index", err)
	if len(result.Stale)+len(result.Leftover)+len(result.Conflicted) != 0 {
		t.Fatalf("result = %+v, want empty", result)
	}
}

// A person's own edit to a policy file predates the command; a restore would
// discard it, so the comparison never names it.
func TestPreexistingEditIsNeverReportedStale(t *testing.T) {
	dir := branchRepo(t)
	write(t, dir, "docs/AGENTS.md", "main\nlocal note\n")
	gittest.Run(t, dir, "add", "--", "docs/AGENTS.md")
	write(t, dir, "docs/AGENTS.md", "main\nlocal note\nunstaged\n")
	snapshot := indexwatch.Take(dir)
	defer snapshot.Release()
	gittest.Run(t, dir, "reset", "-q", "--", "docs/AGENTS.md")

	result, err := snapshot.Stale(t.Context(), policyOnly)
	testutil.FailErr(t, "compare index", err)
	if len(result.Stale) != 0 {
		t.Fatalf("stale = %v; the unstaged edit belongs to the person", result.Stale)
	}
}

func TestRemovedPolicyFileKeptInWorktreeIsLeftover(t *testing.T) {
	dir := branchRepo(t)
	gittest.Run(t, dir, "checkout", "-q", "-b", "without")
	gittest.Run(t, dir, "rm", "-q", "--", "docs/AGENTS.md")
	gittest.Run(t, dir, "commit", "-q", "-m", "Drop instructions")
	gittest.Run(t, dir, "checkout", "-q", "main")
	snapshot := indexwatch.Take(dir)
	defer snapshot.Release()
	lockDir(t, filepath.Join(dir, "docs"))
	gittest.Run(t, dir, "checkout", "-q", "without")

	result, err := snapshot.Stale(t.Context(), policyOnly)
	testutil.FailErr(t, "compare index", err)
	if !slices.Equal(result.Leftover, []string{"docs/AGENTS.md"}) {
		t.Fatalf("leftover = %v, want [docs/AGENTS.md]", result.Leftover)
	}
}

func TestNoRepositoryCapturesNothing(t *testing.T) {
	snapshot := indexwatch.Take(t.TempDir())
	if snapshot.Active() {
		t.Fatal("a folder without Git captured an index")
	}
	snapshot.Release()
}

func TestReleaseRemovesTheCopy(t *testing.T) {
	dir := branchRepo(t)
	snapshot := indexwatch.Take(dir)
	snapshot.Release()
	if snapshot.Active() {
		t.Fatal("released snapshot is still active")
	}
	if _, err := snapshot.Stale(t.Context(), policyOnly); err != nil {
		t.Fatalf("released snapshot comparison: %v", err)
	}
}
