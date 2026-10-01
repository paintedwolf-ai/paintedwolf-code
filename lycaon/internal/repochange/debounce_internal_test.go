package repochange

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestDebouncerOverflowBecomesStructuralResync(t *testing.T) {
	t.Cleanup(ResetObserversForTest)
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) })
	var got Event
	RegisterObserver(func(_ context.Context, ev Event) { got = ev })
	d := NewDebouncer(time.Hour)
	dir := t.TempDir()
	for i := range maxDebouncePaths + 1 {
		d.NotifyWorktreeChanges(context.Background(), dir, []WorktreeChange{{Path: fmt.Sprintf("p-%05d/file", i), Kind: WorktreeChangeContent}}, SourceWatcher)
	}
	d.FlushForTest(t.Context())
	if structural := StructuralPaths(got); !structural.Full {
		t.Fatalf("overflow structural paths = %+v, want full resync", structural)
	}
}

func TestClassifyWatcherChangeUsesNativeEventKinds(t *testing.T) {
	for _, op := range []fsnotify.Op{fsnotify.Create, fsnotify.Remove, fsnotify.Rename, fsnotify.Chmod, fsnotify.Write | fsnotify.Create} {
		if got := classifyWatcherChange(op, false, nil); got != WorktreeChangeStructural {
			t.Fatalf("classifyWatcherChange(%v) = %v, want structural", op, got)
		}
	}
	if got := classifyWatcherChange(fsnotify.Write, false, nil); got != WorktreeChangeContent {
		t.Fatalf("classifyWatcherChange(write) = %v, want content", got)
	}
}

func TestClassifyWatcherWriteUsesStatFacts(t *testing.T) {
	if got := classifyWatcherChange(fsnotify.Write, true, nil); got != WorktreeChangeStructural {
		t.Fatalf("directory write = %v, want structural", got)
	}
	if got := classifyWatcherChange(fsnotify.Write, false, os.ErrNotExist); got != WorktreeChangeUnknown {
		t.Fatalf("missing write = %v, want unknown", got)
	}
}
