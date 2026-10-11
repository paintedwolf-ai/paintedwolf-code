package api

import (
	"context"
	"sync"
)

// ShuttingDown closes when StopBackground runs. Shutdown waits for active
// handlers without canceling their request contexts, so long-lived handlers
// select on this to return.
func (s *Server) ShuttingDown() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.background.Context().Done()
}

// StopBackground cancels detached work before shutdown drains it.
func (s *Server) StopBackground(ctx context.Context) {
	if s == nil {
		return
	}
	if s.fileBriefings != nil {
		s.fileBriefings.Stop()
	}
	s.background.Stop()
	s.Sources.Watch.Stop(ctx)
}

// WaitForBackground drains host work together, then settles its attention updates.
func (s *Server) WaitForBackground(ctx context.Context) {
	if s == nil {
		return
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Sources.Watch.Wait(ctx) }()
	if s.fileBriefings != nil {
		wg.Add(1)
		go func() { defer wg.Done(); s.fileBriefings.Wait(ctx) }()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.background.Wait(ctx)
	}()
	if s.sessions != nil {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.sessions.Coordinator.WaitForTurns(ctx)
		}()
		go func() {
			defer wg.Done()
			s.sessions.Runner.Curation.Wait(ctx)
		}()
	}
	wg.Wait()
	// Detached work may have scheduled an attention rebuild.
	s.eventPublisher.SettleAttention(ctx)
}
