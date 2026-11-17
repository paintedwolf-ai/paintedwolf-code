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
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

// gitWatchFixture is a repository root watched through a scripted platform stream.
type gitWatchFixture struct {
	root, gitDir, branch string
	platform             *errorTestWatcher
	watcher              *worktreeWatcher
}

func newGitWatchFixture(t *testing.T) *gitWatchFixture {
	t.Helper()
	t.Cleanup(ResetObserversForTest)
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) })
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	f := &gitWatchFixture{root: root, gitDir: filepath.Join(root, ".git")}
	f.branch = filepath.Join(f.gitDir, "refs", "heads", "main")
	testutil.FailErr(t, "make refs", os.MkdirAll(filepath.Dir(f.branch), 0o755))
	testutil.FailErr(t, "make logs", os.MkdirAll(filepath.Join(f.gitDir, "logs"), 0o755))
	testutil.FailErr(t, "write HEAD", os.WriteFile(filepath.Join(f.gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	testutil.FailErr(t, "write branch", os.WriteFile(f.branch, []byte(strings.Repeat("a", 40)+"\n"), 0o644))
	f.platform = &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error), resync: make(chan struct{})}
	f.watcher = &worktreeWatcher{root: root, watcher: f.platform, done: make(chan struct{}), watched: map[string]int{root: 1},
		recursive: true, refs: refObserver{root: root, physicalRoot: root}, scope: sourcescope.New(root, sourcescope.Options{})}
	f.watcher.refs.arm(f.watcher.addMeasuredDir)
	finished := make(chan struct{})
	go func() { f.watcher.loop(t.Context()); close(finished) }()
	t.Cleanup(func() { close(f.watcher.done); <-finished })
	return f
}

// settle returns once the loop has handled every delivery sent before it, since
// the loop takes one delivery at a time.
func (f *gitWatchFixture) settle() {
	f.platform.events <- fsnotify.Event{}
}

func (f *gitWatchFixture) headMoveScheduled() bool {
	f.watcher.refs.mu.Lock()
	defer f.watcher.refs.mu.Unlock()
	return f.watcher.refs.timer != nil
}

// `git add` traffic changes the index, its lock and FETCH_HEAD: status is
// invalidated and no head move is scheduled.
func TestIndexChurnSchedulesNoHeadMove(t *testing.T) {
	f := newGitWatchFixture(t)
	indexed := make(chan struct{}, 4)
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir == f.root && event.Kind == IndexChanged {
			indexed <- struct{}{}
		}
	})
	defer unbind()
	for _, name := range []string{"index.lock", "index", "FETCH_HEAD"} {
		path := filepath.Join(f.gitDir, name)
		testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte("x"), 0o644))
		f.platform.events <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	}
	f.settle()
	testutil.Receive(t, "the index change invalidates status", indexed)
	if f.headMoveScheduled() {
		t.Fatal("index churn scheduled HeadMoved")
	}
}

// A head that moves while the stream is dropping events is reported once the
// stream says so, whichever way it says it.
func TestRefsAreReconciledAfterADroppedEventWindow(t *testing.T) {
	f := newGitWatchFixture(t)
	moved := make(chan struct{}, 8)
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir == f.root && event.Kind == HeadMoved {
			moved <- struct{}{}
		}
	})
	defer unbind()

	gaps := []struct {
		name string
		end  func()
	}{
		{"resync", func() { f.platform.resync <- struct{}{} }},
		{"overflow", func() { f.platform.errors <- fsnotify.ErrEventOverflow }},
		{"stream error", func() { f.platform.errors <- errors.New("stream failed") }},
	}
	for i, gap := range gaps {
		// The move's own events are the ones the window dropped.
		testutil.FailErr(t, "move branch", os.WriteFile(f.branch, []byte(strings.Repeat(fmt.Sprint(i), 40)+"\n"), 0o644))
		gap.end()
		f.settle()
		if !f.headMoveScheduled() {
			t.Fatalf("%s: a head moved in the gap and no HeadMoved was scheduled", gap.name)
		}
		testutil.Receive(t, gap.name+": HeadMoved is reported", moved)
	}
}
