package repochange

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDebouncer_CoalescesWorktreeBurst(t *testing.T) {
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) }) // t.Context() is already canceled during cleanup

	var n atomic.Int32
	var lastPaths []string
	var lastSrc Source
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) {
		n.Add(1)
		lastPaths = append([]string(nil), ev.Paths...)
		lastSrc = ev.Source
	}))

	dir := t.TempDir()
	d := newDebouncer(30 * time.Millisecond)
	d.NotifyWorktree(context.Background(), dir, []string{"a.go"}, SourceWatcher)
	d.NotifyWorktree(context.Background(), dir, []string{"b.go"}, SourceMutation)
	d.FlushForTest(t.Context())

	if n.Load() != 1 {
		t.Fatalf("expected 1 coalesced Notify, got %d", n.Load())
	}
	if lastSrc != SourceMutation {
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
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) }) // t.Context() is already canceled during cleanup

	var got Event
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) {
		got = ev
	}))
	dir := t.TempDir()
	NotifyWorktreeDebounced(context.Background(), dir, []string{"x"}, SourceMutation)
	ResetDebouncerForTest(t.Context()) // flush

	if got.Kind != WorktreeChanged {
		t.Fatalf("kind=%v", got.Kind)
	}
	if got.Source != SourceMutation {
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
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) })

	dir := t.TempDir()
	var seen []string
	var queued atomic.Bool
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) {
		seen = append(seen, ev.Paths...)
		// Stands in for the watcher goroutine landing an event mid-flush.
		if queued.CompareAndSwap(false, true) {
			NotifyWorktreeDebounced(
				context.Background(), dir, []string{"late.go"}, SourceWatcher)
		}
	}))

	NotifyWorktreeDebounced(
		context.Background(), dir, []string{"first.go"}, SourceWatcher)
	ResetDebouncerForTest(t.Context())

	got := map[string]bool{}
	for _, p := range seen {
		got[p] = true
	}
	if !got["first.go"] || !got["late.go"] {
		t.Fatalf("paths=%v want both first.go and late.go", seen)
	}
}

func TestDebouncerPreservesStructuredChangeKinds(t *testing.T) {
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) })

	var got Event
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) { got = ev }))
	dir := t.TempDir()
	d := newDebouncer(30 * time.Millisecond)
	d.NotifyWorktreeChanges(context.Background(), dir, []WorktreeChange{{Path: "edit.go", Kind: WorktreeChangeContent}}, SourceWatcher)
	d.NotifyWorktreeChanges(context.Background(), dir, []WorktreeChange{{Path: "new.go", Kind: WorktreeChangeStructural}}, SourceWatcher)
	d.NotifyWorktreeChanges(context.Background(), dir, []WorktreeChange{{Path: "ambiguous.go", Kind: WorktreeChangeContent}}, SourceWatcher)
	d.NotifyWorktree(context.Background(), dir, []string{"ambiguous.go"}, SourceWatcher)
	d.FlushForTest(t.Context())

	structural := StructuralPaths(got)
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
	content := Event{Kind: WorktreeChanged, Changes: []WorktreeChange{{Path: "file.go", Kind: WorktreeChangeContent}}}
	if structural := StructuralPaths(content); structural.Full || len(structural.Paths) != 0 {
		t.Fatalf("content-only structural paths = %+v, want no structural work", structural)
	}
	resync := Event{Kind: WorktreeChanged, Changes: []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}}
	if structural := StructuralPaths(resync); !structural.Full {
		t.Fatalf("resync structural paths = %+v, want full", structural)
	}
	unknown := Event{Kind: WorktreeChanged, Paths: []string{"file.go"}}
	if structural := StructuralPaths(unknown); structural.Full || len(structural.Paths) != 1 || structural.Paths[0] != "file.go" {
		t.Fatalf("unknown structural paths = %+v, want path-conservative", structural)
	}
}
