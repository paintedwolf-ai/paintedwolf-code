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
func (s *Server) StopBackground() {
	if s == nil {
		return
	}
	if s.Sources.FileBriefings != nil {
		s.Sources.FileBriefings.Stop()
	}
	s.background.Stop()
}

// WaitForBackground drains host work together, then settles its attention updates.
func (s *Server) WaitForBackground(ctx context.Context) {
	if s == nil {
		return
	}
	var wg sync.WaitGroup
	if s.Sources.FileBriefings != nil {
		wg.Add(1)
		go func() { defer wg.Done(); s.Sources.FileBriefings.Wait(ctx) }()
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
			s.sessions.WaitForCoordinatorAsyncTurns(ctx)
		}()
		go func() {
			defer wg.Done()
			s.sessions.WaitForPromptCuration()
		}()
	}
	wg.Wait()
	// Detached work may have scheduled an attention rebuild.
	s.eventPublisher.SettleAttention(ctx)
}
