package repochange_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

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
	var headMoved atomic.Int32
	var mu sync.Mutex
	var worktree [][]string
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != root {
			return
		}
		if ev.Kind == repochange.HeadMoved && ev.Source == repochange.SourceWatcher {
			headMoved.Add(1)
		}
		if ev.Kind == repochange.WorktreeChanged {
			// The setup write can arrive again after the platform stream becomes live.
			if len(ev.Paths) == 1 && ev.Paths[0] == watchBarrier {
				return
			}
			mu.Lock()
			worktree = append(worktree, ev.Paths)
			mu.Unlock()
		}
	})

	ensureLiveRoot(t, root)
	// The seeded layout predates the stream; only changes after this point count.
	repochange.ResetDebouncerForTest(t.Context())
	mu.Lock()
	worktree = nil
	mu.Unlock()

	// A commit made in an outside terminal appends the HEAD reflog and moves
	// the loose branch ref. Both also belong to the root metadata namespace.
	gitDir := filepath.Join(root, ".git")
	testutil.FailErr(t, "append reflog", os.WriteFile(filepath.Join(gitDir, "logs", "HEAD"),
		[]byte("aaa bbb author <a@b> 0 +0000\tcommit: outside\n"), 0o644))
	testutil.FailErr(t, "move branch", os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"),
		[]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), 0o644))

	testutil.WaitFor(t, "the outside commit is reported as HeadMoved", func() bool { return headMoved.Load() > 0 })
	repochange.ResetDebouncerForTest(t.Context())
	mu.Lock()
	defer mu.Unlock()
	if len(worktree) != 0 {
		t.Fatalf("lazy Git metadata triggered source filesystem reconciliation: %v", worktree)
	}
}

func TestRefWatchReportsIndexChanges(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })
	t.Cleanup(repochange.ResetWatchersForTest)

	rootDir := t.TempDir()
	seedBareGitLayout(t, rootDir)

	root := canonicalTestDir(t, rootDir)
	var indexChanged atomic.Int32
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir == root && ev.Kind == repochange.IndexChanged {
			indexChanged.Add(1)
		}
	})

	ensureLiveRoot(t, root)

	// `git add` traffic reaches status through the platform stream.
	gitDir := filepath.Join(root, ".git")
	testutil.FailErr(t, "write index lock", os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte("x"), 0o644))
	testutil.FailErr(t, "write index", os.WriteFile(filepath.Join(gitDir, "index"), []byte("x"), 0o644))
	testutil.FailErr(t, "write fetch head", os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), []byte("x"), 0o644))
	testutil.WaitFor(t, "the index change invalidates status", func() bool { return indexChanged.Load() > 0 })
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
	ensureLiveRoot(t, root)
	testutil.FailErr(t, "move shared branch", os.WriteFile(branch, []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), 0o644))
	testutil.WaitFor(t, "the head move is reported", func() bool { return headMoved.Load() > 0 })
}
