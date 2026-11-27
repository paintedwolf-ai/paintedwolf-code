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
// on s.background, outbox delivery scheduling an attention rebuild, or an
// attention flush publishing downstream work). It loops until a full pass
// completes with no new detached work, no outbox rows processed, and no
// attention rebuilds started, or until ctx ends.
func (s *Server) WaitForBackground(ctx context.Context) error {
	if s == nil {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		bgBefore := s.background.Epoch()
		var outboxBefore uint64
		if s.eventOutbox != nil {
			outboxBefore = s.eventOutbox.Epoch()
		}
		var attentionBefore uint64
		if s.eventPublisher != nil {
			attentionBefore = s.eventPublisher.AttentionEpoch()
		}

		var wg sync.WaitGroup
		var watchErr error
		wg.Add(1)
		go func() { defer wg.Done(); watchErr = s.Sources.Watch.Wait(ctx) }()
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
		if watchErr != nil {
			return watchErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Producers above can admit repository analysis. Its settled callback can
		// publish a board before outbox and attention settlement.
		if s.repoProvider != nil {
			if err := s.repoProvider.Wait(ctx); err != nil {
				return err
			}
		}
		// Drain outbox deliveries (and their DELETE commits + OnDelivered hooks)
		// before settling attention rebuilds they may have scheduled.
		if s.eventOutbox != nil {
			if err := s.eventOutbox.Wait(ctx); err != nil {
				return err
			}
		}
		// Detached work and outbox OnDelivered hooks may have scheduled an attention rebuild.
		s.eventPublisher.SettleAttention(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		outboxChanged := s.eventOutbox != nil && s.eventOutbox.Epoch() != outboxBefore
		attentionChanged := s.eventPublisher != nil && s.eventPublisher.AttentionEpoch() != attentionBefore
		if s.background.Epoch() == bgBefore && !s.background.Busy() && !outboxChanged && !attentionChanged {
			return nil
		}
	}
}
