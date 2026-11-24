package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestListWorktreesParsesNulPorcelainRecordsAndSpecialCharacters(t *testing.T) {
	t.Parallel()
	base := gitrepo.CanonicalDir(t.TempDir())
	primary := filepath.Join(base, "primary")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))

	gitCmd := func(dir string, args ...string) {
		t.Helper()
		full := append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-C", dir}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}

	gitCmd(primary, "init", "-q", "-b", "main")
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(primary, "file.txt"), []byte("v1\n"), 0o644))
	gitCmd(primary, "add", "file.txt")
	gitCmd(primary, "commit", "-qm", "init")

	spaced := filepath.Join(base, `sibling "with" spaces`)
	gitCmd(primary, "worktree", "add", "-q", "-b", "feat/spaced", spaced)
	gitCmd(primary, "worktree", "lock", "--reason", "pinned for inspection", spaced)

	detached := filepath.Join(base, "detached-sibling")
	gitCmd(primary, "worktree", "add", "-q", "--detach", detached)

	prunable := filepath.Join(base, "prunable-sibling")
	gitCmd(primary, "worktree", "add", "-q", "-b", "feat/gone", prunable)
	testutil.FailErr(t, "remove prunable directory", os.RemoveAll(prunable))

	mgr := NewManager()
	entries, err := mgr.ListWorktrees(t.Context(), primary)
	testutil.FailErr(t, "list worktrees", err)
	if len(entries) != 4 {
		t.Fatalf("got %d worktree entries, want 4: %+v", len(entries), entries)
	}

	byPath := make(map[string]WorktreeEntry, len(entries))
	for _, entry := range entries {
		byPath[gitrepo.CanonicalDir(entry.Path)] = entry
	}

	primEntry, ok := byPath[primary]
	if !ok || primEntry.Branch != "main" || primEntry.HeadSHA == "" || primEntry.Detached || primEntry.Bare {
		t.Fatalf("unexpected primary entry: %+v (present=%v)", primEntry, ok)
	}
	spacedEntry, ok := byPath[spaced]
	if !ok || spacedEntry.Branch != "feat/spaced" || !spacedEntry.Locked || spacedEntry.LockReason != "pinned for inspection" {
		t.Fatalf("unexpected spaced/locked entry: %+v (present=%v)", spacedEntry, ok)
	}
	detachedEntry, ok := byPath[detached]
	if !ok || !detachedEntry.Detached || detachedEntry.Branch != "" || detachedEntry.HeadSHA == "" {
		t.Fatalf("unexpected detached entry: %+v (present=%v)", detachedEntry, ok)
	}
	prunableEntry, ok := byPath[prunable]
	if !ok || !prunableEntry.Prunable || prunableEntry.Branch != "feat/gone" {
		t.Fatalf("unexpected prunable entry: %+v (present=%v)", prunableEntry, ok)
	}
}
