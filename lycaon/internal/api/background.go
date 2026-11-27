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
	if s.repoProvider != nil {
		s.repoProvider.Stop()
	}
	s.Sources.Watch.Stop(ctx)
}

// WaitForBackground drains host work to a fixed point: subsystems can hand off
// work to each other (for example a watch batch scheduling workspace inventory
// on s.background, or an outbox delivery scheduling an attention rebuild), so
// it repeats until a full pass admits no new detached work.
func (s *Server) WaitForBackground(ctx context.Context) {
	if s == nil {
		return
	}
	const maxSettlePasses = 16
	for range maxSettlePasses {
		if ctx.Err() != nil {
			return
		}
		before := s.background.Epoch()
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
		// Producers above can admit repository analysis. Its settled callback can
		// publish a board before the final attention settlement.
		if s.repoProvider != nil {
			_ = s.repoProvider.Wait(ctx)
		}
		// Drain outbox deliveries (and their DELETE commits + OnDelivered hooks)
		// before settling attention rebuilds they may have scheduled.
		if s.eventOutbox != nil {
			_ = s.eventOutbox.Wait(ctx)
		}
		// Detached work may have scheduled an attention rebuild.
		s.eventPublisher.SettleAttention(ctx)
		if s.background.Epoch() == before && !s.background.Busy() {
			return
		}
	}
}
