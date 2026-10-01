package repochange

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/watchfd"
)

// Stop cannot recall a timer whose function has already started, so the
// notification checks the close itself.
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

	// The debounce window is already running when the close lands — the race a
	// stopped timer cannot win.
	w.scheduleHeadNotify(context.Background())
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	time.Sleep(refNotifyDebounce + 150*time.Millisecond)
	if moved.Load() != 0 {
		t.Fatalf("closed watcher emitted HeadMoved x%d", moved.Load())
	}

	// A ref event arriving after the close arms nothing at all.
	w.scheduleHeadNotify(context.Background())
	w.mu.Lock()
	armed := w.refNotifyTimer != nil
	w.mu.Unlock()
	if armed {
		t.Fatal("closed watcher armed a fresh HeadMoved timer")
	}
}
