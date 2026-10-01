// Package taskgroup tracks API background work through cancellation and draining.
package taskgroup

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/observability"
)

type Group struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	active   int
	idle     chan struct{}
	services sync.WaitGroup
}

// GoService runs a loop that lives as long as the server. Wait drains it only
// after Stop, so an idle wait never blocks on a service.
func (s *Group) GoService(fn func(ctx context.Context)) {
	ctx := s.Context()
	s.services.Go(func() {
		defer observability.GuardPanic("api.service")
		fn(ctx)
	})
}

// Go runs tracked work until server shutdown.
func (s *Group) Go(parent context.Context, fn func(ctx context.Context)) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	stop := context.AfterFunc(s.Context(), cancel) //nolint:contextcheck // Server shutdown triggers cancellation.
	s.mu.Lock()
	if s.active == 0 {
		s.idle = make(chan struct{})
	}
	s.active++
	s.mu.Unlock()
	go func() {
		defer s.finish()
		defer cancel()
		defer stop()
		defer observability.GuardPanic("api.detached")
		fn(ctx)
	}()
}

func (s *Group) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if s.active == 0 {
		close(s.idle)
	}
}

// Wait drains detached work; each busy interval has its own completion
// channel. After Stop it also drains services.
func (s *Group) Wait(ctx context.Context) {
	s.mu.Lock()
	done := s.idle
	idle := s.active == 0
	s.mu.Unlock()
	if !idle {
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
	if s.Context().Err() == nil {
		return
	}
	stopped := make(chan struct{})
	go func() { s.services.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-ctx.Done():
	}
}

func (s *Group) Context() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}
	return s.ctx
}

func (s *Group) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}
	s.cancel()
}
