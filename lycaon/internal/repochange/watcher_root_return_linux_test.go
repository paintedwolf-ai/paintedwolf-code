//go:build linux

package repochange

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

// TestLinuxRepeatedRootReturnRebindsAndRetires verifies that repeated real
// move-away-and-back (IN_MOVE_SELF) on a watched Linux root transitions the
// watcher out of false-live into faulted, rebinds a single fresh inotify watch
// without resource growth when the same-inode root returns, delivers post-return
// writes, and retires the watch cleanly on CloseRoot.
func TestLinuxRepeatedRootReturnRebindsAndRetires(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve parent", err)
	root := filepath.Join(parent, "root0")
	testutil.FailErr(t, "mkdir root0", os.Mkdir(root, 0o755))
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed\n"), 0o644))

	reg := &watcherRegistry{
		byDir:   map[string]*worktreeWatcher{},
		maxDirs: 64,
		budget:  watchfd.NewBudget(64),
	}
	t.Cleanup(reg.CloseAll)

	var mu sync.Mutex
	var changes []WorktreeChange
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir != root || event.Kind != WorktreeChanged || event.Source != SourceWatcher {
			return
		}
		mu.Lock()
		changes = append(changes, event.Changes...)
		mu.Unlock()
	})
	t.Cleanup(unbind)

	reg.Ensure(t.Context(), root)
	testutil.WaitFor(t, "initial root watch complete", func() bool {
		cov := reg.Coverage(root)
		return cov.Watching && !cov.Faulted && cov.Complete && cov.Watched == 1
	})

	origInfo, err := os.Stat(root)
	testutil.FailErr(t, "stat original root", err)

	for round := 1; round <= 3; round++ {
		ResetDebouncerForTest(t.Context())
		mu.Lock()
		changes = nil
		mu.Unlock()
		moved := filepath.Join(parent, fmt.Sprintf("root0-moved-%d", round))
		testutil.FailErr(t, "move root away", os.Rename(root, moved))
		testutil.FailErr(t, "move root back", os.Rename(moved, root))
		// Observe the loss before asking Coverage or Ensure, even when the
		// path already resolves to the original directory again.
		testutil.WaitFor(t, fmt.Sprintf("round %d publishes the outage", round), func() bool {
			ResetDebouncerForTest(t.Context())
			mu.Lock()
			defer mu.Unlock()
			return slices.ContainsFunc(changes, func(ch WorktreeChange) bool {
				return ch.Kind == WorktreeChangeResync
			})
		})
		cov := reg.Coverage(root)
		if !cov.Watching || !cov.Faulted || cov.Complete || cov.Watched != 0 {
			t.Fatalf("round %d: root coverage after loss = %+v", round, cov)
		}
		returnedInfo, err := os.Stat(root)
		testutil.FailErr(t, "stat returned root", err)
		if !os.SameFile(origInfo, returnedInfo) {
			t.Fatalf("round %d: expected same-inode root return", round)
		}

		// Ensure must rebind the returned same-inode root with no watch growth.
		reg.Ensure(t.Context(), root)
		testutil.WaitFor(t, fmt.Sprintf("round %d recovers live coverage", round), func() bool {
			cov := reg.Coverage(root)
			return cov.Watching && !cov.Faulted && cov.Complete && cov.Watched == 1
		})

		mu.Lock()
		changes = nil
		mu.Unlock()
		rel := fmt.Sprintf("edit_%d.txt", round)
		testutil.FailErr(t, "write post-return file", os.WriteFile(filepath.Join(root, rel), []byte("ok\n"), 0o644))
		testutil.WaitFor(t, fmt.Sprintf("round %d delivers %s", round, rel), func() bool {
			ResetDebouncerForTest(t.Context())
			mu.Lock()
			defer mu.Unlock()
			return slices.ContainsFunc(changes, func(ch WorktreeChange) bool {
				return ch.Path == rel
			})
		})
	}

	// Watch retirement on removal must drop coverage and leave no residual watch.
	reg.CloseRoot(root)
	if cov := reg.Coverage(root); cov.Watching || cov.Watched != 0 {
		t.Fatalf("coverage after CloseRoot = %+v, want Watching=false Watched=0", cov)
	}
	sharedFsnotify.ops.Lock()
	fw := sharedFsnotify.fw
	sharedFsnotify.ops.Unlock()
	if fw != nil && fw.Has(streamPath(root)) {
		t.Fatalf("backend stream still holds watch for closed root %s", root)
	}
}
