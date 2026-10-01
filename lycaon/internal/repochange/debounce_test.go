package repochange_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDebouncer_CoalescesWorktreeBurst(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) }) // t.Context() is already canceled during cleanup

	var n atomic.Int32
	var lastPaths []string
	var lastSrc repochange.Source
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		n.Add(1)
		lastPaths = append([]string(nil), ev.Paths...)
		lastSrc = ev.Source
	})

	dir := t.TempDir()
	d := repochange.NewDebouncer(30 * time.Millisecond)
	d.NotifyWorktree(context.Background(), dir, []string{"a.go"}, repochange.SourceWatcher)
	d.NotifyWorktree(context.Background(), dir, []string{"b.go"}, repochange.SourceMutation)
	d.FlushForTest(t.Context())

	if n.Load() != 1 {
		t.Fatalf("expected 1 coalesced Notify, got %d", n.Load())
	}
	if lastSrc != repochange.SourceMutation {
		t.Fatalf("expected mutation preference, got %q", lastSrc)
	}
	got := map[string]bool{}
	for _, p := range lastPaths {
		got[p] = true
	}
	if !got["a.go"] || !got["b.go"] {
		t.Fatalf("paths=%v", lastPaths)
	}
}

func TestNotifyWorktreeDebounced_EmitsWithSource(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) }) // t.Context() is already canceled during cleanup

	var got repochange.Event
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		got = ev
	})
	dir := t.TempDir()
	repochange.NotifyWorktreeDebounced(context.Background(), dir, []string{"x"}, repochange.SourceMutation)
	repochange.ResetDebouncerForTest(t.Context()) // flush

	if got.Kind != repochange.WorktreeChanged {
		t.Fatalf("kind=%v", got.Kind)
	}
	if got.Source != repochange.SourceMutation {
		t.Fatalf("source=%q", got.Source)
	}
	abs, err := filepath.Abs(dir)
	testutil.FailErr(t, "abs", err)
	if got.ProjectDir != abs {
		t.Fatalf("ProjectDir=%q want %q", got.ProjectDir, abs)
	}
}

// Flush callbacks may queue more work while the lock is released.
func TestFlushForTestDeliversWorkQueuedDuringTheFlush(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })

	dir := t.TempDir()
	var seen []string
	var queued atomic.Bool
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		seen = append(seen, ev.Paths...)
		// Stands in for the watcher goroutine landing an event mid-flush.
		if queued.CompareAndSwap(false, true) {
			repochange.NotifyWorktreeDebounced(
				context.Background(), dir, []string{"late.go"}, repochange.SourceWatcher)
		}
	})

	repochange.NotifyWorktreeDebounced(
		context.Background(), dir, []string{"first.go"}, repochange.SourceWatcher)
	repochange.ResetDebouncerForTest(t.Context())

	got := map[string]bool{}
	for _, p := range seen {
		got[p] = true
	}
	if !got["first.go"] || !got["late.go"] {
		t.Fatalf("paths=%v want both first.go and late.go", seen)
	}
}

func TestDebouncerPreservesStructuredChangeKinds(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })

	var got repochange.Event
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) { got = ev })
	dir := t.TempDir()
	d := repochange.NewDebouncer(30 * time.Millisecond)
	d.NotifyWorktreeChanges(context.Background(), dir, []repochange.WorktreeChange{{Path: "edit.go", Kind: repochange.WorktreeChangeContent}}, repochange.SourceWatcher)
	d.NotifyWorktreeChanges(context.Background(), dir, []repochange.WorktreeChange{{Path: "new.go", Kind: repochange.WorktreeChangeStructural}}, repochange.SourceWatcher)
	d.NotifyWorktreeChanges(context.Background(), dir, []repochange.WorktreeChange{{Path: "ambiguous.go", Kind: repochange.WorktreeChangeContent}}, repochange.SourceWatcher)
	d.NotifyWorktree(context.Background(), dir, []string{"ambiguous.go"}, repochange.SourceWatcher)
	d.FlushForTest(t.Context())

	structural := repochange.StructuralPaths(got)
	if structural.Full {
		t.Fatal("coalesced paths requested full structural resync")
	}
	want := map[string]bool{"new.go": true, "ambiguous.go": true}
	for _, path := range structural.Paths {
		delete(want, path)
	}
	if len(want) != 0 {
		t.Fatalf("structural paths = %v, missing %v", structural.Paths, want)
	}
}

func TestStructuralPathsDistinguishesContentOnlyFromResync(t *testing.T) {
	content := repochange.Event{Kind: repochange.WorktreeChanged, Changes: []repochange.WorktreeChange{{Path: "file.go", Kind: repochange.WorktreeChangeContent}}}
	if structural := repochange.StructuralPaths(content); structural.Full || len(structural.Paths) != 0 {
		t.Fatalf("content-only structural paths = %+v, want no structural work", structural)
	}
	resync := repochange.Event{Kind: repochange.WorktreeChanged, Changes: []repochange.WorktreeChange{{Path: ".", Kind: repochange.WorktreeChangeResync}}}
	if structural := repochange.StructuralPaths(resync); !structural.Full {
		t.Fatalf("resync structural paths = %+v, want full", structural)
	}
	unknown := repochange.Event{Kind: repochange.WorktreeChanged, Paths: []string{"file.go"}}
	if structural := repochange.StructuralPaths(unknown); structural.Full || len(structural.Paths) != 1 || structural.Paths[0] != "file.go" {
		t.Fatalf("unknown structural paths = %+v, want path-conservative", structural)
	}
}
