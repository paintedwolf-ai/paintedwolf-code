package sourcefeed

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func firePending(t *testing.T, c *changeConverger) <-chan struct{} {
	t.Helper()
	c.mu.Lock()
	c.timer.Stop()
	generation := c.generation
	c.mu.Unlock()
	done := make(chan struct{})
	go func() { defer close(done); c.fire(t.Context(), generation) }()
	return done
}

func requireConvergerBusy(t *testing.T, c *changeConverger) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("busy converger wait = %v", err)
	}
}

func TestChangeConvergerWaitIncludesQueuedAndOverlappingPublication(t *testing.T) {
	entered := make(chan string, 3)
	first, second := make(chan struct{}), make(chan struct{})
	releaseFirst, releaseSecond := sync.OnceFunc(func() { close(first) }), sync.OnceFunc(func() { close(second) })
	c := newChangeConverger(time.Hour, time.Hour, func(_ context.Context, batch pendingExternalBatch) {
		path := batch.changes[0].rel
		entered <- path
		if path == "first" {
			<-first
		}
		if path == "second" {
			<-second
		}
	})
	t.Cleanup(func() { c.close(t.Context()) })
	t.Cleanup(releaseFirst)
	t.Cleanup(releaseSecond)
	root := RootSpec{ID: "root"}
	c.queue(t.Context(), root, "first")
	requireConvergerBusy(t, c)
	firstDone := firePending(t, c)
	if path := <-entered; path != "first" {
		t.Fatal(path)
	}
	requireConvergerBusy(t, c)
	c.queue(t.Context(), root, "second")
	secondDone := firePending(t, c)
	if path := <-entered; path != "second" {
		t.Fatal(path)
	}
	releaseFirst()
	<-firstDone
	requireConvergerBusy(t, c)
	releaseSecond()
	<-secondDone
	if err := c.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.queue(t.Context(), root, "third")
	requireConvergerBusy(t, c)
	<-firePending(t, c)
	if path := <-entered; path != "third" {
		t.Fatal(path)
	}
	if err := c.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestChangeConvergerCloseJoinsPendingPublication(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	t.Cleanup(finish)
	c := newChangeConverger(time.Hour, time.Hour, func(context.Context, pendingExternalBatch) { close(entered); <-release })
	c.queueResync(t.Context())
	closed := make(chan struct{})
	go func() { defer close(closed); c.close(t.Context()) }()
	<-entered
	requireConvergerBusy(t, c)
	finish()
	<-closed
	if err := c.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.queueResync(t.Context())
	if err := c.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWatchLifetimeWaitIncludesQueuedBatch(t *testing.T) {
	lifetime := &WatchLifetime{}
	c := newChangeConverger(time.Hour, time.Hour, func(context.Context, pendingExternalBatch) {})
	key := watchKey{"queued-lifetime", ""}
	watchRegMu.Lock()
	watchers[key] = &projectWatch{lifetime: lifetime, changes: c}
	watchRegMu.Unlock()
	t.Cleanup(func() { watchRegMu.Lock(); delete(watchers, key); watchRegMu.Unlock(); c.close(t.Context()) })
	c.queueResync(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := lifetime.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued lifetime wait = %v", err)
	}
	<-firePending(t, c)
	if err := lifetime.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWatchLifetimeStopJoinsConvergerPublication(t *testing.T) {
	lifetime := &WatchLifetime{}
	entered, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { lifetime.Stop(t.Context()); _ = lifetime.Wait(t.Context()) })
	t.Cleanup(finish)
	roots := []RootSpec{{ID: "root", WorkspaceID: "ws", Path: t.TempDir()}}
	EnsureProjectWatch(t.Context(), lifetime, "queued-stop", "", roots, func(ctx context.Context, _ string, _ ExternalBatch) { close(entered); <-release })
	watchRegMu.Lock()
	c := watchers[watchKey{"queued-stop", ""}].changes
	watchRegMu.Unlock()
	c.queueResync(t.Context())
	done := firePending(t, c)
	<-entered
	lifetime.Stop(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := lifetime.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping lifetime lost publication = %v", err)
	}
	finish()
	<-done
	if err := lifetime.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}
