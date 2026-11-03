package git

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func selectedCommitFixture(t *testing.T) string {
	t.Helper()
	return CloneFixtureRepo(t, "selected-commit", func(dir string) {
		gittest.Init(t, dir)
		for path, content := range map[string]string{"kept.txt": "kept\n", "old.txt": "moved\n", "cached.txt": "cached\n", "peer.txt": "peer\n"} {
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644))
		}
		gittest.CommitAll(t, dir, "base")
		testutil.FailErr(t, "peer edit", os.WriteFile(filepath.Join(dir, "peer.txt"), []byte("staged peer\n"), 0o644))
		gittest.Run(t, dir, "add", "--", "peer.txt")
	})
}

func TestSelectedCommitKeepsStagedDeletionsAndRenames(t *testing.T) {
	t.Parallel()
	dir := selectedCommitFixture(t)
	gittest.Run(t, dir, "rm", "-q", "--cached", "--", "cached.txt")
	gittest.Run(t, dir, "mv", "--", "old.txt", "new.txt")
	mgr := NewManager()
	changed, err := mgr.ChangedPaths(t.Context(), dir)
	testutil.FailErr(t, "changed paths", err)
	for _, want := range []string{"new.txt", "old.txt", "cached.txt"} {
		if !slices.Contains(changed, want) {
			t.Fatalf("changed paths %v lack %s", changed, want)
		}
	}
	_, err = mgr.Commit(t.Context(), dir, GitCommitOpts{Message: "remove and rename", Paths: []string{"cached.txt", "new.txt", "old.txt"}})
	testutil.FailErr(t, "commit", err)
	if got := gittest.Run(t, dir, "show", "--format=", "--name-status", "-M", "HEAD"); got != "D\tcached.txt\nR100\told.txt\tnew.txt\n" {
		t.Fatalf("committed changes %q", got)
	}
	if got := gittest.Run(t, dir, "status", "--short"); got != "M  peer.txt\n?? cached.txt\n" {
		t.Fatalf("status after commit %q", got)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "cached.txt")); err != nil || string(data) != "cached\n" {
		t.Fatalf("cached removal changed the working file: %q %v", data, err)
	}
}

func TestSelectedCommitTakesWorkingContentAndAmends(t *testing.T) {
	t.Parallel()
	dir := selectedCommitFixture(t)
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(dir, "kept.txt"), []byte("edited\n"), 0o644))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(dir, "old.txt")))
	mgr := NewManager()
	_, err := mgr.Commit(t.Context(), dir, GitCommitOpts{Message: "edit", Paths: []string{"kept.txt", "old.txt"}})
	testutil.FailErr(t, "commit", err)
	parent := gittest.Run(t, dir, "rev-parse", "HEAD^")
	testutil.FailErr(t, "edit again", os.WriteFile(filepath.Join(dir, "kept.txt"), []byte("amended\n"), 0o644))
	_, err = mgr.Commit(t.Context(), dir, GitCommitOpts{Message: "edit amended", Paths: []string{"kept.txt"}, Amend: true})
	testutil.FailErr(t, "amend", err)
	if got := gittest.Run(t, dir, "rev-parse", "HEAD^"); got != parent {
		t.Fatalf("amend parent %q, want %q", got, parent)
	}
	if got := gittest.Run(t, dir, "show", "--format=", "--name-status", "HEAD"); got != "M\tkept.txt\nD\told.txt\n" {
		t.Fatalf("amended changes %q", got)
	}
	if got := gittest.Run(t, dir, "show", "HEAD:kept.txt"); got != "amended\n" {
		t.Fatalf("amended content %q", got)
	}
	if got := gittest.Run(t, dir, "status", "--short"); got != "M  peer.txt\n" {
		t.Fatalf("status after amend %q", got)
	}
}

func TestSelectedCommitBeforeTheFirstCommit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gittest.Init(t, dir)
	for _, path := range []string{"first.txt", "other.txt"} {
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, path), []byte(path+"\n"), 0o644))
	}
	gittest.Run(t, dir, "add", "--", "other.txt")
	_, err := NewManager().Commit(t.Context(), dir, GitCommitOpts{Message: "first", Paths: []string{"first.txt"}})
	testutil.FailErr(t, "first commit", err)
	if got := gittest.Run(t, dir, "ls-tree", "--name-only", "HEAD"); got != "first.txt\n" {
		t.Fatalf("first commit tree %q", got)
	}
	if got := gittest.Run(t, dir, "status", "--short"); got != "A  other.txt\n" {
		t.Fatalf("status after first commit %q", got)
	}
}

func TestSelectedCommitKeepsExplicitIdentity(t *testing.T) {
	t.Parallel()
	dir := selectedCommitFixture(t)
	testutil.FailErr(t, "edit selected file", os.WriteFile(filepath.Join(dir, "kept.txt"), []byte("selected\n"), 0o644))
	_, err := NewManager().Commit(t.Context(), dir, GitCommitOpts{
		Message: "selected identity", Paths: []string{"kept.txt"},
		IdentityName: "Selected author", IdentityEmail: "selected@example.invalid",
	})
	testutil.FailErr(t, "commit selected file", err)
	if got := gittest.Run(t, dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != "Selected author <selected@example.invalid>|Selected author <selected@example.invalid>\n" {
		t.Fatalf("commit identity = %q", got)
	}
	if got := gittest.Run(t, dir, "status", "--short"); got != "M  peer.txt\n" {
		t.Fatalf("unrelated staged work = %q", got)
	}
}
