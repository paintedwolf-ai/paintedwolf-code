package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetachedDrainOverlapsRepeatedAdmissions(t *testing.T) {
	s := &Server{}
	t.Cleanup(s.StopBackground)
	var completed atomic.Int64
	var callers sync.WaitGroup
	for range 8 {
		callers.Go(func() {
			for range 250 {
				s.background.Go(t.Context(), func(context.Context) { completed.Add(1) })
				s.background.Wait(t.Context())
			}
		})
	}
	callers.Wait()
	s.WaitForBackground(t.Context())
	if got := completed.Load(); got != 2000 {
		t.Fatalf("drained callbacks = %d, want 2000", got)
	}
}

func TestDetachedDrainIncludesNestedWorkAndHonorsCancellation(t *testing.T) {
	s := &Server{}
	t.Cleanup(s.StopBackground)
	childStarted := make(chan struct{})
	release := make(chan struct{})
	s.background.Go(t.Context(), func(ctx context.Context) {
		s.background.Go(ctx, func(context.Context) {
			close(childStarted)
			<-release
		})
	})
	<-childStarted
	drained := make(chan struct{})
	go func() { s.WaitForBackground(t.Context()); close(drained) }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.WaitForBackground(ctx)
	select {
	case <-drained:
		t.Fatal("drain returned while nested detached work was active")
	default:
	}
	close(release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not return after nested work completed")
	}
}
