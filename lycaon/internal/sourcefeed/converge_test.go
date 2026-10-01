package sourcefeed

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestChangeConvergerCollapsesWritesWithoutClassifyingPaths(t *testing.T) {
	flushed := make(chan pendingExternalBatch, 1)
	c := newChangeConverger(10*time.Millisecond, 50*time.Millisecond, func(_ context.Context, changes pendingExternalBatch) {
		flushed <- changes
	})
	t.Cleanup(func() { c.close(t.Context()) })
	root := RootSpec{ID: "root", WorkspaceID: "ws", Path: t.TempDir()}
	for range 20 {
		c.queue(t.Context(), root, ".ignored/unknown-language.scratch")
	}
	select {
	case batch := <-flushed:
		changes := batch.changes
		if batch.resync || len(changes) != 1 || changes[0].rel != ".ignored/unknown-language.scratch" {
			t.Fatalf("converged changes = %+v", changes)
		}
	case <-time.After(time.Second):
		t.Fatal("converged change was not flushed")
	}
}

func TestChangeConvergerFlushesContinuousWritesAtMaximumDelay(t *testing.T) {
	flushed := make(chan pendingExternalBatch, 1)
	c := newChangeConverger(40*time.Millisecond, 80*time.Millisecond, func(_ context.Context, changes pendingExternalBatch) {
		flushed <- changes
	})
	t.Cleanup(func() { c.close(t.Context()) })
	root := RootSpec{ID: "root", WorkspaceID: "ws", Path: t.TempDir()}
	deadline := time.Now().Add(140 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.queue(t.Context(), root, "continuous.data")
		select {
		case batch := <-flushed:
			changes := batch.changes
			if batch.resync || len(changes) != 1 || changes[0].rel != "continuous.data" {
				t.Fatalf("maximum-delay changes = %+v", changes)
			}
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("continuous writes starved convergence")
}

func TestChangeConvergerKeepsEveryDistinctPath(t *testing.T) {
	flushed := make(chan pendingExternalBatch, 1)
	c := newChangeConverger(10*time.Millisecond, 50*time.Millisecond, func(_ context.Context, changes pendingExternalBatch) {
		flushed <- changes
	})
	t.Cleanup(func() { c.close(t.Context()) })
	root := RootSpec{ID: "root", WorkspaceID: "ws", Path: t.TempDir()}
	c.queue(t.Context(), root, "z.any")
	c.queue(t.Context(), root, "a.any")
	select {
	case batch := <-flushed:
		changes := batch.changes
		if batch.resync || len(changes) != 2 || changes[0].rel != "a.any" || changes[1].rel != "z.any" {
			t.Fatalf("distinct changes = %+v", changes)
		}
	case <-time.After(time.Second):
		t.Fatal("distinct changes were not flushed")
	}
}

func TestChangeConvergerBoundsDistinctPathsAndRequestsResync(t *testing.T) {
	flushed := make(chan pendingExternalBatch, 1)
	c := newChangeConverger(time.Hour, time.Hour, func(_ context.Context, batch pendingExternalBatch) {
		flushed <- batch
	})
	root := RootSpec{ID: "root", WorkspaceID: "ws", Path: t.TempDir()}
	for i := 0; i <= maxChangesPerEvent; i++ {
		c.queue(t.Context(), root, time.Unix(int64(i), 0).Format(time.RFC3339Nano))
	}
	c.close(t.Context())
	batch := <-flushed
	// Past the cap the window is one invalidation; holding a partial list
	// would only cost consumers the size of the churn.
	if !batch.resync || len(batch.changes) != 0 {
		t.Fatalf("overflow batch = {changes:%d resync:%t}", len(batch.changes), batch.resync)
	}
}

func TestChangeConvergerRecoversPathsAfterOverflowFlush(t *testing.T) {
	flushed := make(chan pendingExternalBatch, 2)
	c := newChangeConverger(time.Hour, time.Hour, func(_ context.Context, batch pendingExternalBatch) {
		flushed <- batch
	})
	root := RootSpec{ID: "root", WorkspaceID: "ws", Path: t.TempDir()}
	for i := 0; i <= maxChangesPerEvent; i++ {
		c.queue(t.Context(), root, time.Unix(int64(i), 0).Format(time.RFC3339Nano))
	}
	c.fire(t.Context(), c.generation)
	c.queue(t.Context(), root, "after.txt")
	c.close(t.Context())
	<-flushed
	batch := <-flushed
	if batch.resync || len(batch.changes) != 1 || batch.changes[0].rel != "after.txt" {
		t.Fatalf("window after overflow = {changes:%+v resync:%t}", batch.changes, batch.resync)
	}
}

// A held window outlasts the maximum delay and flushes once the hold lifts.
func TestChangeConvergerHoldsTheWindowWhileHeld(t *testing.T) {
	flushed := make(chan pendingExternalBatch, 1)
	c := newChangeConverger(10*time.Millisecond, 30*time.Millisecond, func(_ context.Context, batch pendingExternalBatch) {
		flushed <- batch
	})
	t.Cleanup(func() { c.close(t.Context()) })
	var held atomic.Bool
	held.Store(true)
	c.holdWhile(held.Load, time.Second)
	root := RootSpec{ID: "root", WorkspaceID: "ws", Path: t.TempDir()}
	c.queue(t.Context(), root, "first.txt")
	select {
	case batch := <-flushed:
		t.Fatalf("held window flushed: %+v", batch)
	case <-time.After(120 * time.Millisecond):
	}
	c.queue(t.Context(), root, "second.txt")
	c.queueHeadMoved(t.Context())
	held.Store(false)
	select {
	case batch := <-flushed:
		if len(batch.changes) != 2 || !batch.headMoved {
			t.Fatalf("released window = {changes:%d headMoved:%t}", len(batch.changes), batch.headMoved)
		}
	case <-time.After(time.Second):
		t.Fatal("released window was not flushed")
	}
}

// The hold has a ceiling, so a lock left behind cannot stall observation.
func TestChangeConvergerHoldCeilingFlushesAnyway(t *testing.T) {
	flushed := make(chan pendingExternalBatch, 1)
	c := newChangeConverger(10*time.Millisecond, 30*time.Millisecond, func(_ context.Context, batch pendingExternalBatch) {
		flushed <- batch
	})
	t.Cleanup(func() { c.close(t.Context()) })
	c.holdWhile(func() bool { return true }, 60*time.Millisecond)
	c.queue(t.Context(), RootSpec{ID: "root", WorkspaceID: "ws", Path: t.TempDir()}, "stuck.txt")
	select {
	case batch := <-flushed:
		if len(batch.changes) != 1 {
			t.Fatalf("ceiling flush = %+v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("held window never reached its ceiling")
	}
}

func TestChangeConvergerFlushesPathlessResync(t *testing.T) {
	flushed := make(chan pendingExternalBatch, 1)
	c := newChangeConverger(10*time.Millisecond, 50*time.Millisecond, func(_ context.Context, batch pendingExternalBatch) {
		flushed <- batch
	})
	t.Cleanup(func() { c.close(t.Context()) })
	c.queueResync(t.Context())
	select {
	case batch := <-flushed:
		if !batch.resync || len(batch.changes) != 0 {
			t.Fatalf("pathless resync = {changes:%d resync:%t}", len(batch.changes), batch.resync)
		}
	case <-time.After(time.Second):
		t.Fatal("pathless resync was not flushed")
	}
}
