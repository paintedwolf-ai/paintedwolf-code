package repochange

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/testutil"
)

// limitTestWatcher refuses registrations with the configured error.
type limitTestWatcher struct {
	errorTestWatcher
	addErr atomic.Pointer[error]
}

func (w *limitTestWatcher) Add(string) error {
	if err := w.addErr.Load(); err != nil {
		return *err
	}
	return nil
}

func TestWatcherOverflowResyncsAndKeepsTheStream(t *testing.T) {
	root := t.TempDir()
	platform := &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error, 1), resync: make(chan struct{})}
	w := &worktreeWatcher{root: root, watcher: platform, done: make(chan struct{}), watched: map[string]int{root: 1}}
	var resyncs atomic.Int32
	var incompleteDuringDelivery atomic.Bool
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir != root || event.Kind != WorktreeChanged || len(event.Paths) != 1 || event.Paths[0] != "." {
			return
		}
		resyncs.Add(1)
		if !w.coverage().Complete {
			incompleteDuringDelivery.Store(true)
		}
	})
	defer unbind()
	before := CurrentEpoch(root)
	finished := make(chan struct{})
	go func() { w.loop(t.Context()); close(finished) }()
	defer func() { close(w.done); <-finished }()
	platform.errors <- fsnotify.ErrEventOverflow
	testutil.WaitFor(t, time.Second, func() bool { return resyncs.Load() == 1 })
	if !incompleteDuringDelivery.Load() {
		t.Fatal("coverage claimed completeness while the dropped window was still open")
	}
	if EpochCurrent(root, before) {
		t.Fatal("overflow resync did not advance the worktree epoch")
	}
	testutil.WaitFor(t, time.Second, func() bool { return w.coverage().Complete })
	coverage := w.coverage()
	if coverage.Faulted || coverage.Overflows != 1 {
		t.Fatalf("overflow coverage = %+v, want one closed overflow on a live stream", coverage)
	}
	platform.errors <- errors.New("watch stream failed")
	testutil.WaitFor(t, time.Second, func() bool { return w.coverage().Faulted })
	if w.coverage().Complete {
		t.Fatal("stream fault retained complete coverage")
	}
}

func TestWatcherReportsTheUserWatchLimitUntilRegistrationSucceeds(t *testing.T) {
	root := t.TempDir()
	platform := &limitTestWatcher{errorTestWatcher: errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error), resync: make(chan struct{})}}
	w := &worktreeWatcher{root: root, watcher: platform, done: make(chan struct{}), watched: map[string]int{root: 1}, maxDirs: 16}
	exhausted := error(syscall.ENOSPC)
	platform.addErr.Store(&exhausted)
	if outcome := w.addDir(filepath.Join(root, "first"), 1); outcome != addFailed {
		t.Fatalf("refused registration outcome = %v, want addFailed", outcome)
	}
	if coverage := w.coverage(); !coverage.WatchLimit {
		t.Fatalf("coverage = %+v, want the watch limit fact", coverage)
	}
	denied := error(syscall.EACCES)
	platform.addErr.Store(&denied)
	if outcome := w.addDir(filepath.Join(root, "second"), 1); outcome != addFailed {
		t.Fatalf("denied registration outcome = %v, want addFailed", outcome)
	}
	if coverage := w.coverage(); !coverage.WatchLimit {
		t.Fatal("an unrelated refusal cleared the watch limit fact")
	}
	platform.addErr.Store(nil)
	if outcome := w.addDir(filepath.Join(root, "third"), 1); outcome != addRegistered {
		t.Fatalf("registration outcome = %v, want addRegistered", outcome)
	}
	if coverage := w.coverage(); coverage.WatchLimit || coverage.Watched != 2 {
		t.Fatalf("coverage after capacity returned = %+v", coverage)
	}
}
