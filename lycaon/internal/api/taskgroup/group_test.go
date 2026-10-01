package taskgroup

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Detached goroutines need their own panic recovery.
func TestGoDetachedSurvivesPanic(t *testing.T) {
	s := &Group{}

	s.Go(context.Background(), func(context.Context) {
		panic("detached turn blew up")
	})

	// Panics still release the tracked goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Wait(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("detached work never drained after a panicking detached goroutine")
	}

	// Still usable afterwards: the guard recovers the goroutine, not the server.
	ran := make(chan struct{})
	s.Go(context.Background(), func(context.Context) { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not run work scheduled after a panic")
	}
	s.Wait(context.Background())
}

func TestGoDetachedOutlivesRequestAndStopsWithServer(t *testing.T) {
	s := &Group{}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan error, 1)
	s.Go(requestCtx, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		stopped <- ctx.Err()
	})
	<-started
	cancelRequest()

	select {
	case err := <-stopped:
		t.Fatalf("request cancellation stopped detached work: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	s.Stop()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server shutdown did not stop detached work")
	}
	s.Wait(context.Background())
}

// A running service never holds an idle wait; after Stop the wait drains it.
func TestServicesDrainOnlyAfterStop(t *testing.T) {
	s := &Group{}
	closed := make(chan struct{})
	s.GoService(func(ctx context.Context) {
		<-ctx.Done()
		close(closed)
	})
	idle, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.Wait(idle)
	if idle.Err() != nil {
		t.Fatal("an idle wait blocked on a running service")
	}
	s.Stop()
	s.Wait(context.Background())
	select {
	case <-closed:
	default:
		t.Fatal("wait after stop returned before the service finished")
	}
}
