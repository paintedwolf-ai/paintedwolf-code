package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestResolveRootGitDirsPreventsCrossRootAndPrefixWidening(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	wtA := filepath.Join(base, "wt-a")
	wtB := filepath.Join(base, "wt-b")
	impostor := filepath.Join(base, "impostor")

	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(primary, "file.txt"), []byte("base\n"), 0o644))
	gittest.InitCommit(t, primary, "init")
	gittest.Run(t, primary, "worktree", "add", "-b", "branch-a", wtA)
	gittest.Run(t, primary, "worktree", "add", "-b", "branch-b", wtB)

	adminA := filepath.Join(primary, ".git", "worktrees", "wt-a")
	adminB := filepath.Join(primary, ".git", "worktrees", "wt-b")
	commonDir := filepath.Join(primary, ".git")

	testutil.FailErr(t, "mkdir impostor", os.MkdirAll(impostor, 0o755))
	testutil.FailErr(t, "write impostor .git", os.WriteFile(filepath.Join(impostor, ".git"), []byte("gitdir: "+adminA+"\n"), 0o644))

	txn, err := BeginTransaction(TransactionConfig{
		Dir: filepath.Join(t.TempDir(), "tx"),
		ID:  "root-scoped",
		AuthorizedRootGitDirs: map[string]AuthorizedGitDir{
			"root-a": {GitDir: adminA, CommonDir: commonDir},
		},
	})
	testutil.FailErr(t, "BeginTransaction", err)

	// 1. Exact match for root-a succeeds.
	gotGit, gotCommon, err := txn.resolveRootGitDirs("root-a", wtA)
	testutil.FailErr(t, "resolveRootGitDirs root-a", err)
	if !sameCanonicalPath(gotGit, adminA) || !sameCanonicalPath(gotCommon, commonDir) {
		t.Fatalf("got (%s, %s), want (%s, %s)", gotGit, gotCommon, adminA, commonDir)
	}

	// 2. Cross-root: root-b pointing to root-a's admin dir is refused.
	if _, _, err := txn.resolveRootGitDirs("root-b", impostor); !errors.Is(err, ErrUnauthorizedGitDir) {
		t.Fatalf("cross-root impostor err = %v, want ErrUnauthorizedGitDir", err)
	}

	// 3. Prefix widening: root-a pointing to sibling worktree under the same commonDir is refused.
	testutil.FailErr(t, "repoint wt-a to adminB", os.WriteFile(filepath.Join(wtA, ".git"), []byte("gitdir: "+adminB+"\n"), 0o644))
	if _, _, err := txn.resolveRootGitDirs("root-a", wtA); !errors.Is(err, ErrUnauthorizedGitDir) {
		t.Fatalf("prefix-widened sibling worktree err = %v, want ErrUnauthorizedGitDir", err)
	}
}
