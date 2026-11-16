package repochange

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A head that moves while the stream is dropping events is reported once the
// stream says so, whichever way it says it.
func TestRefsAreReconciledAfterADroppedEventWindow(t *testing.T) {
	t.Cleanup(ResetObserversForTest)
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) })
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	gitDir := filepath.Join(root, ".git")
	testutil.FailErr(t, "make refs", os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755))
	testutil.FailErr(t, "make logs", os.MkdirAll(filepath.Join(gitDir, "logs"), 0o755))
	testutil.FailErr(t, "write HEAD", os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	branch := filepath.Join(gitDir, "refs", "heads", "main")
	testutil.FailErr(t, "write branch", os.WriteFile(branch, []byte(strings.Repeat("a", 40)+"\n"), 0o644))

	platform := &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error), resync: make(chan struct{})}
	w := &worktreeWatcher{root: root, watcher: platform, done: make(chan struct{}), watched: map[string]int{root: 1},
		recursive: true, refs: refObserver{root: root, physicalRoot: root}}
	w.refs.arm(w.addMeasuredDir)
	moved := make(chan struct{}, 8)
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir == root && event.Kind == HeadMoved {
			moved <- struct{}{}
		}
	})
	defer unbind()
	finished := make(chan struct{})
	go func() { w.loop(t.Context()); close(finished) }()
	defer func() { close(w.done); <-finished }()

	gaps := []struct {
		name string
		end  func()
	}{
		{"resync", func() { platform.resync <- struct{}{} }},
		{"overflow", func() { platform.errors <- fsnotify.ErrEventOverflow }},
		{"stream error", func() { platform.errors <- errors.New("stream failed") }},
	}
	for i, gap := range gaps {
		// The move's own events are the ones the window dropped.
		testutil.FailErr(t, "move branch", os.WriteFile(branch, []byte(strings.Repeat(fmt.Sprint(i), 40)+"\n"), 0o644))
		gap.end()
		// The loop handles one delivery at a time, so this one starts after the gap's handling ended.
		platform.events <- fsnotify.Event{}
		w.refs.mu.Lock()
		scheduled := w.refs.timer != nil
		w.refs.mu.Unlock()
		if !scheduled {
			t.Fatalf("%s: a head moved in the gap and no HeadMoved was scheduled", gap.name)
		}
		testutil.Receive(t, gap.name+": HeadMoved is reported", moved)
	}
}
