package hitl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExpiryTimersFireUntilStopped(t *testing.T) {
	var timers expiryTimers
	fired := make(chan string, 1)
	expire := func(_ context.Context, checkpointID, reason string) error {
		fired <- checkpointID + ": " + reason
		return nil
	}
	timers.schedule(t.Context(), "disabled", 0, expire)
	timers.schedule(t.Context(), "first", time.Millisecond, expire)
	select {
	case got := <-fired:
		if got != "first: "+expiryReason {
			t.Fatalf("expiry fired %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("armed expiry never fired")
	}

	timers.schedule(t.Context(), "pending", time.Hour, expire)
	if err := timers.stop(t.Context()); err != nil {
		t.Fatalf("drain expiry timers: %v", err)
	}
	timers.mu.Lock()
	armed := len(timers.timers)
	timers.mu.Unlock()
	if armed != 0 {
		t.Fatalf("%d expiries still armed after stop", armed)
	}

	timers.schedule(t.Context(), "late", time.Millisecond, expire)
	select {
	case got := <-fired:
		t.Fatalf("expiry %q fired after stop", got)
	case <-time.After(50 * time.Millisecond):
	}
	var manager *Checkpoints
	if err := manager.StopExpiryTimers(t.Context()); err != nil {
		t.Fatalf("drain expiry timers: %v", err)
	}
}

func TestExpiryStopDrainsRunningCallback(t *testing.T) {
	var timers expiryTimers
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	timers.schedule(t.Context(), "running", time.Millisecond, func(context.Context, string, string) error {
		close(started)
		<-release
		return nil
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not start")
	}
	go func() { finished <- timers.stop(t.Context()) }()
	select {
	case err := <-finished:
		t.Fatalf("stop returned before callback drain: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("drain expiry timers: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not drain")
	}
}

func TestExpiryDrainHonorsShutdownCancellation(t *testing.T) {
	var timers expiryTimers
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	timers.schedule(t.Context(), "running", time.Millisecond, func(context.Context, string, string) error {
		close(started)
		<-release
		return nil
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := timers.stop(ctx); err != context.Canceled {
		t.Fatalf("cancelled shutdown drain = %v", err)
	}
	close(release)
	if err := timers.stop(t.Context()); err != nil {
		t.Fatalf("drain expiry timers: %v", err)
	}
}

func TestCanceledExpiryDrainKeepsPersistenceUntilRetry(t *testing.T) {
	var timers expiryTimers
	started, release, callbackDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		testutil.FailErr(t, "final expiry drain", timers.stop(context.Background()))
	})
	timers.schedule(t.Context(), "running", time.Millisecond, func(context.Context, string, string) error {
		close(started)
		<-release
		close(callbackDone)
		return nil
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("expiry callback did not start")
	}
	registry, scope := resourcelifecycle.New(), resourcelifecycle.DeviceScope()
	stopping := make(chan struct{}, 1)
	testutil.FailErr(t, "track expiry drain", registry.Track(scope, "checkpoint-expiries", 25, func(ctx context.Context, _ resourcelifecycle.Scope) error {
		stopping <- struct{}{}
		return timers.stop(ctx)
	}))
	persistenceClosed := false
	testutil.FailErr(t, "track persistence", registry.Track(scope, "database", 130, func(context.Context, resourcelifecycle.Scope) error {
		select {
		case <-callbackDone:
		default:
			t.Error("persistence closed while expiry callback was still active")
		}
		persistenceClosed = true
		return nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- registry.Dispose(ctx, scope) }()
	select {
	case <-stopping:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not start expiry drain")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled drain did not return")
	}
	if persistenceClosed {
		t.Fatal("canceled shutdown advanced to persistence")
	}
	close(release)
	testutil.FailErr(t, "retry ordered shutdown", registry.Dispose(t.Context(), scope))
	if !persistenceClosed {
		t.Fatal("retry lost pending persistence cleanup")
	}
}
