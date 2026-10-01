package repochange_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

// seedBareGitLayout writes the .git structure the ref watcher arms on, without
// running git: HEAD, a loose branch, and the reflog directory.
func seedBareGitLayout(t *testing.T, root string) {
	t.Helper()
	gitDir := filepath.Join(root, ".git")
	testutil.FailErr(t, "mk refs", os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755))
	testutil.FailErr(t, "mk logs", os.MkdirAll(filepath.Join(gitDir, "logs"), 0o755))
	testutil.FailErr(t, "write HEAD",
		os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	testutil.FailErr(t, "write branch",
		os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"),
			[]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o644))
	testutil.FailErr(t, "write reflog",
		os.WriteFile(filepath.Join(gitDir, "logs", "HEAD"), []byte(""), 0o644))
}

func TestRefWatchEmitsHeadMovedForOutsideCommit(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })
	t.Cleanup(repochange.ResetWatchersForTest)

	rootDir := t.TempDir()
	seedBareGitLayout(t, rootDir)

	root := canonicalTestDir(t, rootDir)
	var headMoved, worktree atomic.Int32
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != root {
			return
		}
		if ev.Kind == repochange.HeadMoved && ev.Source == repochange.SourceWatcher {
			headMoved.Add(1)
		}
		if ev.Kind == repochange.WorktreeChanged {
			worktree.Add(1)
		}
	})

	repochange.EnsureRoot(t.Context(), root)
	time.Sleep(50 * time.Millisecond)

	// A commit made in an outside terminal appends the HEAD reflog and moves
	// the loose branch ref. Both also belong to the root metadata namespace.
	gitDir := filepath.Join(root, ".git")
	testutil.FailErr(t, "append reflog", os.WriteFile(filepath.Join(gitDir, "logs", "HEAD"),
		[]byte("aaa bbb author <a@b> 0 +0000\tcommit: outside\n"), 0o644))
	testutil.FailErr(t, "move branch", os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"),
		[]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), 0o644))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && headMoved.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if headMoved.Load() == 0 {
		t.Fatal("expected HeadMoved for a ref-surface write")
	}
	repochange.ResetDebouncerForTest(t.Context())
	if worktree.Load() == 0 {
		t.Fatal("ref metadata did not notify filesystem consumers")
	}
}

func TestRefWatchReportsIndexChangesWithoutHeadMovement(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })
	t.Cleanup(repochange.ResetWatchersForTest)

	rootDir := t.TempDir()
	seedBareGitLayout(t, rootDir)

	root := canonicalTestDir(t, rootDir)
	var headMoved atomic.Int32
	var indexChanged atomic.Int32
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != root {
			return
		}
		if ev.Kind == repochange.HeadMoved {
			headMoved.Add(1)
		}
		if ev.Kind == repochange.IndexChanged {
			indexChanged.Add(1)
		}
	})

	repochange.EnsureRoot(t.Context(), root)
	time.Sleep(50 * time.Millisecond)

	// `git add` traffic: the index and its lock are not movement surfaces.
	gitDir := filepath.Join(root, ".git")
	testutil.FailErr(t, "write index lock", os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte("x"), 0o644))
	testutil.FailErr(t, "write index", os.WriteFile(filepath.Join(gitDir, "index"), []byte("x"), 0o644))
	testutil.FailErr(t, "write fetch head", os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), []byte("x"), 0o644))

	time.Sleep(500 * time.Millisecond)
	if headMoved.Load() != 0 {
		t.Fatalf("index churn produced HeadMoved x%d", headMoved.Load())
	}
	if indexChanged.Load() == 0 {
		t.Fatal("index change did not invalidate status")
	}
}

func TestRefWatchEmitsForLinkedWorktreeCommonDirectory(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })
	t.Cleanup(repochange.ResetWatchersForTest)

	root := t.TempDir()
	common := t.TempDir()
	gitDir := filepath.Join(common, "worktrees", "linked")
	testutil.FailErr(t, "mkdir git logs", os.MkdirAll(filepath.Join(gitDir, "logs"), 0o755))
	testutil.FailErr(t, "mkdir common refs", os.MkdirAll(filepath.Join(common, "refs", "heads"), 0o755))
	testutil.FailErr(t, "write git pointer", os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644))
	testutil.FailErr(t, "write commondir", os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644))
	testutil.FailErr(t, "write HEAD", os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	testutil.FailErr(t, "write reflog", os.WriteFile(filepath.Join(gitDir, "logs", "HEAD"), nil, 0o644))
	branch := filepath.Join(common, "refs", "heads", "main")
	testutil.FailErr(t, "write branch", os.WriteFile(branch, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o644))

	canonical := canonicalTestDir(t, root)
	var headMoved atomic.Int32
	repochange.RegisterObserver(func(_ context.Context, event repochange.Event) {
		if event.ProjectDir == canonical && event.Kind == repochange.HeadMoved {
			headMoved.Add(1)
		}
	})
	repochange.EnsureRoot(t.Context(), root)
	time.Sleep(50 * time.Millisecond)
	testutil.FailErr(t, "move shared branch", os.WriteFile(branch, []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), 0o644))
	testutil.WaitFor(t, 2*time.Second, func() bool { return headMoved.Load() > 0 })
}
