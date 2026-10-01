package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestCommitAmendPreservesHistoryAndUnrelatedIndex(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	write := func(path, content string) {
		t.Helper()
		testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644))
	}
	write("peer.txt", "original\n")
	write("remove.txt", "obsolete\n")
	gittest.CommitAll(t, dir, "base")
	base := gittest.Run(t, dir, "rev-parse", "HEAD")
	write("feature.txt", "feature\n")
	gittest.CommitAll(t, dir, "feature")
	head := gittest.Run(t, dir, "rev-parse", "HEAD")
	write("peer.txt", "peer work\n")
	gittest.Run(t, dir, "add", "--", "peer.txt")
	write("-new_test.txt", "regression\n")
	testutil.FailErr(t, "remove fixture", os.Remove(filepath.Join(dir, "remove.txt")))

	mgr := NewManager()
	_, commitErr := mgr.Commit(t.Context(), dir, GitCommitOpts{
		Message: "feature with regression", Paths: []string{"-new_test.txt", "remove.txt"}, Amend: true,
	})
	testutil.FailErr(t, "amend with new file", commitErr)
	if got := gittest.Run(t, dir, "rev-parse", "HEAD"); got == head {
		t.Fatal("amend did not replace HEAD")
	}
	if got := gittest.Run(t, dir, "rev-parse", "HEAD^"); got != base {
		t.Fatalf("parent = %q, want %q", got, base)
	}
	for path, want := range map[string]string{
		"feature.txt": "feature\n", "peer.txt": "original\n", "-new_test.txt": "regression\n",
	} {
		if got := gittest.Run(t, dir, "show", "HEAD:"+path); got != want {
			t.Errorf("committed %s = %q, want %q", path, got, want)
		}
	}
	if got := gittest.Run(t, dir, "ls-tree", "--name-only", "HEAD", "--", "remove.txt"); got != "" {
		t.Errorf("deleted path remains in commit: %q", got)
	}
	if got := strings.TrimSpace(gittest.Run(t, dir, "log", "-1", "--format=%s")); got != "feature with regression" {
		t.Errorf("message = %q", got)
	}
	if got := gittest.Run(t, dir, "diff", "--cached", "--name-only"); got != "peer.txt\n" {
		t.Errorf("remaining staged files = %q, want peer.txt", got)
	}
}

func TestCommitAmendRequiresScopeBeforeStaging(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644))
	_, err := NewManager().Commit(t.Context(), dir, GitCommitOpts{Message: "amend", Amend: true})
	if err == nil {
		t.Fatal("amend without paths succeeded")
	}
	if got := gittest.Run(t, dir, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("unscoped amend staged files: %q", got)
	}
}
