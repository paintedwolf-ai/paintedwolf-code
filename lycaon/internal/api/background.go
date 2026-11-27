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

// WaitForBackground drains every host background participant until a pass
// admits no new work, or returns ctx's error.
func (s *Server) WaitForBackground(ctx context.Context) error {
	if s == nil {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		watchBefore := s.Sources.Watch.Epoch()
		briefingBefore := s.fileBriefings.Epoch()
		bgBefore := s.background.Epoch()
		var coordBefore, curationBefore uint64
		if s.sessions != nil {
			coordBefore = s.sessions.Coordinator.TurnEpoch()
			if s.sessions.Runner != nil {
				curationBefore = s.sessions.Runner.Curation.Epoch()
			}
		}
		var repoBefore uint64
		if s.repoProvider != nil {
			repoBefore = s.repoProvider.Epoch()
		}
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
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.sessions.Coordinator.WaitForTurns(ctx)
			}()
			if s.sessions.Runner != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s.sessions.Runner.Curation.Wait(ctx)
				}()
			}
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
		// Check idle states before epochs so a handoff landing after an idle
		// check still shows up as an epoch change.
		curationBusy := s.sessions != nil && s.sessions.Runner != nil && s.sessions.Runner.Curation.Busy()
		busy := s.Sources.Watch.Busy() ||
			s.fileBriefings.Busy() ||
			s.background.Busy() ||
			(s.sessions != nil && s.sessions.Coordinator.TurnBusy()) ||
			curationBusy ||
			(s.repoProvider != nil && s.repoProvider.Busy()) ||
			(s.eventOutbox != nil && s.eventOutbox.Busy()) ||
			(s.eventPublisher != nil && s.eventPublisher.AttentionBusy())
		if busy {
			continue
		}
		curationMoved := s.sessions != nil && s.sessions.Runner != nil && s.sessions.Runner.Curation.Epoch() != curationBefore
		moved := s.Sources.Watch.Epoch() != watchBefore ||
			s.fileBriefings.Epoch() != briefingBefore ||
			s.background.Epoch() != bgBefore ||
			(s.sessions != nil && s.sessions.Coordinator.TurnEpoch() != coordBefore) ||
			curationMoved ||
			(s.repoProvider != nil && s.repoProvider.Epoch() != repoBefore) ||
			(s.eventOutbox != nil && s.eventOutbox.Epoch() != outboxBefore) ||
			(s.eventPublisher != nil && s.eventPublisher.AttentionEpoch() != attentionBefore)
		if !moved {
			return nil
		}
	}
}
