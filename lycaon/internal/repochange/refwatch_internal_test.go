package repochange

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/watchfd"
)

// A running timer callback must observe closure.
func TestScheduleHeadNotifyStaysSilentOnceClosed(t *testing.T) {
	root := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		var moved atomic.Int32
		unbind := RegisterObserver(func(_ context.Context, ev Event) {
			if ev.Kind == HeadMoved {
				moved.Add(1)
			}
		})
		defer unbind()
		refs := &refObserver{root: root, physicalRoot: root}

		// Leave the timer running to exercise the callback fence.
		refs.schedule(context.Background())
		refs.mu.Lock()
		refs.closed = true
		refs.mu.Unlock()
		time.Sleep(2 * refNotifyDebounce)
		synctest.Wait()
		if moved.Load() != 0 {
			t.Fatalf("closed watcher emitted HeadMoved x%d", moved.Load())
		}

		// A ref event arriving after the close arms nothing at all.
		refs.schedule(context.Background())
		refs.mu.Lock()
		armed := refs.timer != nil
		refs.mu.Unlock()
		if armed {
			t.Fatal("closed watcher armed a fresh HeadMoved timer")
		}
	})
}

// The root reaches its repository through a symlink, as macOS temp and home
// paths do; ref surfaces must carry the root's names, not the resolved ones.
func TestReferenceRemovalPreservesWorktreeCoverage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatalf("create root alias: %v", err)
	}
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatalf("create reference directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("create HEAD: %v", err)
	}
	w, err := newWorktreeWatcher(t.Context(), root, watchfd.MaxDirs, watchfd.NewProcessBudget())
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer w.close()
	head := filepath.Join(gitDir, "HEAD")
	if !w.refs.contains(head) {
		t.Fatal("HEAD is not registered")
	}
	w.forgetRemovedTree(gitDir)
	if w.refs.contains(head) {
		t.Fatal("removed reference surface remains registered")
	}
	if !w.watchesDir(root) {
		t.Fatal("reference removal discarded worktree coverage")
	}
}
