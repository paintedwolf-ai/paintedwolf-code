package persistence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLedgerGitPositionDistinguishesAbsentUnbornCommittedAndBrokenRepositories(t *testing.T) {
	root := t.TempDir()
	reader := gitStateReader{}
	if got := reader.HeadState(t.Context(), root); got.Repo != gitstate.RepoAbsent {
		t.Fatalf("plain directory position=%+v", got)
	}
	gitDir := filepath.Join(root, ".git")
	testutil.FailErr(t, "create repository refs", os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0700))
	testutil.FailErr(t, "create repository objects", os.MkdirAll(filepath.Join(gitDir, "objects"), 0700))
	testutil.FailErr(t, "write branch identity", os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0600))
	if got := reader.HeadState(t.Context(), root); got.Repo != gitstate.RepoPresent || got.HeadCommit != "" || got.HeadRef != "main" {
		t.Fatalf("unborn repository position=%+v", got)
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	testutil.FailErr(t, "write committed branch", os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte(commit+"\n"), 0600))
	if got := reader.HeadState(t.Context(), root); got.Repo != gitstate.RepoPresent || got.HeadCommit != commit || got.HeadRef != "main" {
		t.Fatalf("committed repository position=%+v", got)
	}
	testutil.FailErr(t, "remove unreadable head", os.Remove(filepath.Join(gitDir, "HEAD")))
	if got := reader.HeadState(t.Context(), root); got.Repo != gitstate.RepoUnreadable || got.HeadCommit != "" {
		t.Fatalf("broken repository was reported absent or committed:%+v", got)
	}
}
