//go:build darwin && cgo

package repochange

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFSEventsWatcherIsRecursiveWithoutPerFileDescriptors(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src", "generated")
	testutil.FailErr(t, "mkdir fixture", os.MkdirAll(nested, 0o755))
	for i := range 1000 {
		name := filepath.Join(nested, time.Unix(int64(i), 0).Format("150405.000000000"))
		testutil.FailErr(t, "write fixture", os.WriteFile(name, []byte("fixture"), 0o644))
	}
	before := openFileDescriptorCount(t)
	w, err := newPlatformWatcher(root)
	testutil.FailErr(t, "new watcher", err)
	t.Cleanup(func() { _ = w.Close() })
	testutil.FailErr(t, "add root", w.Add(root))
	testutil.FailErr(t, "add nested", w.Add(nested))
	after := openFileDescriptorCount(t)
	if delta := after - before; delta > 16 {
		t.Fatalf("FSEvents opened %d descriptors for a 1,000-file tree", delta)
	}

	target := filepath.Join(nested, "fresh.go")
	testutil.FailErr(t, "write nested file", os.WriteFile(target, []byte("package fresh"), 0o644))
	wantWatcherEvent(t, w, target)
}

func TestFSEventsWatcherExtendsToExternalGitMetadata(t *testing.T) {
	root := t.TempDir()
	common := t.TempDir()
	refs := filepath.Join(common, "refs", "heads")
	testutil.FailErr(t, "mkdir refs", os.MkdirAll(refs, 0o755))
	target := filepath.Join(refs, "main")
	testutil.FailErr(t, "seed external ref", os.WriteFile(target, []byte("cafebabe\n"), 0o644))
	w, err := newPlatformWatcher(root)
	testutil.FailErr(t, "new watcher", err)
	t.Cleanup(func() { _ = w.Close() })
	testutil.FailErr(t, "add root", w.Add(root))
	testutil.FailErr(t, "add external refs", w.Add(refs))

	// Restarting an FSEvents stream is synchronous, but leave one latency window
	// before the mutation so this test exercises only post-registration events.
	time.Sleep(2 * fseventsLatency)
	testutil.FailErr(t, "write external ref", os.WriteFile(target, []byte("deadbeef\n"), 0o644))
	wantWatcherEvent(t, w, target)
}

func TestFSEventsWatcherCloseDoesNotDependOnConsumerDrain(t *testing.T) {
	root := t.TempDir()
	w, err := newPlatformWatcher(root)
	testutil.FailErr(t, "new watcher", err)
	testutil.FailErr(t, "add root", w.Add(root))
	for i := range fseventsQueueLimit + 512 {
		name := filepath.Join(root, time.Unix(int64(i), 0).Format("150405.000000000"))
		testutil.FailErr(t, "write burst", os.WriteFile(name, []byte("x"), 0o644))
	}
	time.Sleep(2 * fseventsLatency)
	closed := make(chan struct{})
	go func() {
		_ = w.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked behind an undrained event consumer")
	}
}

func wantWatcherEvent(t *testing.T, w platformWatcher, target string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	var seen []fsnotify.Event
	for {
		select {
		case event := <-w.EventChannel():
			seen = append(seen, event)
			if event.Name == target && event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) != 0 {
				return
			}
		case <-deadline.C:
			t.Fatalf("no event for %q; received %+v", target, seen)
		}
	}
}

func openFileDescriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("descriptor listing unavailable: %v", err)
	}
	return len(entries)
}
