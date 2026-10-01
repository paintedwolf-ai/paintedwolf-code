package repochange

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/testutil"
)

type errorTestWatcher struct {
	events chan fsnotify.Event
	errors chan error
	resync chan struct{}
}

func (*errorTestWatcher) Add(string) error                      { return nil }
func (*errorTestWatcher) Remove(string) error                   { return nil }
func (*errorTestWatcher) Close() error                          { return nil }
func (*errorTestWatcher) WatchList() []string                   { return nil }
func (w *errorTestWatcher) EventChannel() <-chan fsnotify.Event { return w.events }
func (w *errorTestWatcher) ErrorChannel() <-chan error          { return w.errors }
func (w *errorTestWatcher) ResyncChannel() <-chan struct{}      { return w.resync }

func TestWatcherErrorInvalidatesRootAndCoverage(t *testing.T) {
	root := t.TempDir()
	platform := &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error, 1), resync: make(chan struct{})}
	w := &worktreeWatcher{root: root, watcher: platform, done: make(chan struct{}), watched: map[string]int{root: 1}}
	var observed atomic.Bool
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir == root && event.Kind == WorktreeChanged && len(event.Paths) == 1 && event.Paths[0] == "." {
			observed.Store(true)
		}
	})
	defer unbind()
	finished := make(chan struct{})
	go func() { w.loop(t.Context()); close(finished) }()
	defer func() { close(w.done); <-finished }()
	platform.errors <- errors.New("watch stream failed")
	testutil.WaitFor(t, time.Second, func() bool {
		ResetDebouncerForTest(t.Context())
		return observed.Load()
	})
	w.mu.Lock()
	coverage := w.coverageLocked()
	w.mu.Unlock()
	if coverage.Complete {
		t.Fatal("watcher error retained complete coverage")
	}
}
