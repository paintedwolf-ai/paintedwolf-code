package repochange

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/watchfd"
)

// A running timer callback must observe closure.
func TestScheduleHeadNotifyStaysSilentOnceClosed(t *testing.T) {
	t.Cleanup(ResetObserversForTest)

	root := t.TempDir()
	w, err := newWorktreeWatcher(t.Context(), root, watchfd.MaxDirs, watchfd.NewProcessBudget())
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	t.Cleanup(w.close)

	var moved atomic.Int32
	RegisterObserver(func(_ context.Context, ev Event) {
		if ev.Kind == HeadMoved {
			moved.Add(1)
		}
	})

	// Leave the timer running to exercise the callback fence.
	w.refs.schedule(context.Background(), root)
	w.refs.mu.Lock()
	w.refs.closed = true
	w.refs.mu.Unlock()
	time.Sleep(refNotifyDebounce + 150*time.Millisecond)
	if moved.Load() != 0 {
		t.Fatalf("closed watcher emitted HeadMoved x%d", moved.Load())
	}

	// A ref event arriving after the close arms nothing at all.
	w.refs.schedule(context.Background(), root)
	w.refs.mu.Lock()
	armed := w.refs.timer != nil
	w.refs.mu.Unlock()
	if armed {
		t.Fatal("closed watcher armed a fresh HeadMoved timer")
	}
}

func TestReferenceRemovalPreservesWorktreeCoverage(t *testing.T) {
	root := t.TempDir()
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
