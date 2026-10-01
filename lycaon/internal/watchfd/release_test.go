package watchfd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

// openFDs counts the process's open descriptors. Reading the directory opens one
// itself, which is constant across calls and so cancels out of a comparison.
func openFDs(t *testing.T) int {
	t.Helper()
	dir := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		dir = "/dev/fd"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no descriptor listing at %s: %v", dir, err)
	}
	return len(entries)
}

// Closing a watcher returns its descriptors to the operating system, not only to
// the budget. This guards fsnotify's kqueue Close, which can leak every watch
// descriptor (fsnotify #732).
func TestClosingWatcherReleasesDescriptors(t *testing.T) {
	if watchfd.Cost(2) == watchfd.Cost(1) {
		t.Skip("watch costs one unit per directory: no per-entry descriptors to leak")
	}

	root := t.TempDir()
	for d := range 4 {
		dir := filepath.Join(root, fmt.Sprintf("d%d", d))
		testutil.FailErr(t, "mkdir tree dir", os.Mkdir(dir, 0o755))
		for f := range 32 {
			name := filepath.Join(dir, fmt.Sprintf("f%02d", f))
			testutil.FailErr(t, "write tree file", os.WriteFile(name, []byte("x"), 0o644))
		}
	}

	before := openFDs(t)

	fw, err := fsnotify.NewWatcher()
	testutil.FailErr(t, "new watcher", err)
	testutil.FailErr(t, "watch root", fw.Add(root))
	for d := range 4 {
		testutil.FailErr(t, "watch dir", fw.Add(filepath.Join(root, fmt.Sprintf("d%d", d))))
	}

	held := openFDs(t)
	if held <= before {
		t.Skipf("watching opened no descriptors on this platform (%d -> %d)", before, held)
	}

	testutil.FailErr(t, "close watcher", fw.Close())

	if after := openFDs(t); after > before {
		t.Fatalf("descriptors after close = %d, was %d before watching (peaked at %d): "+
			"a closed watcher still holds %d files, so watching a repository leaks the "+
			"engine's file handles", after, before, held, after-before)
	}
}
