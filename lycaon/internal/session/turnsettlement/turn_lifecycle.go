package turnsettlement

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/pkg/api"
)

type deferredTurnSettlement struct {
	disposition     api.SessionIdleDisposition
	closeoutBlocked *promptresult.CloseoutBlocked
}

type deferredTurnSettlementStore struct {
	mu              sync.Mutex
	bySession       map[string]deferredTurnSettlement
	closeoutBlocked map[string]*promptresult.CloseoutBlocked
}

func (s *deferredTurnSettlementStore) noteCloseoutBlocked(sessionID string, blocked *promptresult.CloseoutBlocked) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeoutBlocked == nil {
		s.closeoutBlocked = make(map[string]*promptresult.CloseoutBlocked)
	}
	s.closeoutBlocked[sessionID] = blocked
}

func (s *deferredTurnSettlementStore) takeCloseoutBlocked(sessionID string) *promptresult.CloseoutBlocked {
	s.mu.Lock()
	defer s.mu.Unlock()
	blocked := s.closeoutBlocked[sessionID]
	delete(s.closeoutBlocked, sessionID)
	return blocked
}

func (s *deferredTurnSettlementStore) put(sessionID string, disposition api.SessionIdleDisposition) {
	s.putEntry(sessionID, deferredTurnSettlement{disposition: disposition})
}

func (s *deferredTurnSettlementStore) putEntry(sessionID string, entry deferredTurnSettlement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bySession == nil {
		s.bySession = make(map[string]deferredTurnSettlement)
	}
	s.bySession[sessionID] = entry
}

func (s *deferredTurnSettlementStore) takeEntry(sessionID string) (deferredTurnSettlement, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.bySession[sessionID]
	delete(s.bySession, sessionID)
	return entry, ok
}

func (s *deferredTurnSettlementStore) remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bySession, sessionID)
	delete(s.closeoutBlocked, sessionID)
}

// NoteCloseoutBlocked records that the active turn settled as closeout_blocked
// so Finish and SettlePending keep the workflow resumable instead of failing the turn.
func (m *Service) NoteCloseoutBlocked(sessionID string, blocked *promptresult.CloseoutBlocked) {
	if m != nil && blocked != nil {
		m.deferredTurnSettlement.noteCloseoutBlocked(sessionID, blocked)
	}
}

func (m *Service) Begin(sessionID string, pendingKickIDs ...string) {
	if m != nil {
		m.deferredTurnSettlement.takeCloseoutBlocked(sessionID)
		m.batch.BeginTurn(sessionID)
		m.runtime.BeginPromptTurn(sessionID, pendingKickIDs...)
	}
}

func (m *Service) End(sessionID string) {
	if m != nil {
		m.runtime.EndPromptTurn(sessionID)
	}
}

// Finish records the next visible-turn transition.
func (m *Service) Finish(ctx context.Context, sessionID string, promptFailed, hostTurn bool, closeoutID string) error {
	if m == nil {
		return nil
	}
	// Host cleanup ignores client cancellation.
	hostCtx := context.WithoutCancel(ctx)
	rt := m.runtime
	m.End(sessionID)
	closeoutBlocked := m.deferredTurnSettlement.takeCloseoutBlocked(sessionID)
	if m.gate.InProgress(hostCtx, sessionID) {
		m.deferredTurnSettlement.remove(sessionID)
		rt.CoordinatorLoop().Nudges.ClearPending(sessionID)
		rt.Kicks().ClearPending(sessionID)
		return nil
	}
	if promptFailed {
		m.deferredTurnSettlement.put(sessionID, m.Disposition(true))
		return nil
	}
	if m.workflows != nil {
		if err := m.workflows.Phases.ReconcileTurnCompletion(hostCtx, sessionID); err != nil {
			return m.Failure(sessionID, fmt.Errorf("reconcile workflow turn completion: %w", err))
		}
	}
	if closeoutBlocked != nil {
		m.batch.Reconcile(hostCtx, sessionID)
		m.batch.DisarmTerminal(hostCtx, sessionID)
		if m.workflows != nil {
			if err := m.workflows.Reports.MaybeDeliverTopologyReport(hostCtx, sessionID, closeoutID); err != nil {
				return m.Failure(sessionID, fmt.Errorf("deliver topology report: %w", err))
			}
			m.DisarmInactiveWorkflow(hostCtx, sessionID)
		}
		m.deferredTurnSettlement.putEntry(sessionID, deferredTurnSettlement{
			disposition:     api.SessionIdleDispositionCompleted,
			closeoutBlocked: closeoutBlocked,
		})
		return nil
	}
	continuation := rt.CoordinatorLoop().Waits.OnTurnComplete(
		hostCtx, sessionID, hostTurn,
	)
	m.batch.Reconcile(hostCtx, sessionID)
	m.batch.DisarmTerminal(hostCtx, sessionID)
	if m.workflows != nil {
		if err := m.workflows.Reports.MaybeDeliverTopologyReport(hostCtx, sessionID, closeoutID); err != nil {
			return m.Failure(sessionID, fmt.Errorf("deliver topology report: %w", err))
		}
		m.DisarmInactiveWorkflow(hostCtx, sessionID)
	}
	if continuation == loopwake.UserTurnContinues {
		m.deferredTurnSettlement.remove(sessionID)
		return nil
	}
	if m.workflows != nil {
		if err := m.workflows.Policy.AssertSessionCompletion(hostCtx, sessionID); err != nil {
			return m.Failure(sessionID, err)
		}
	}
	m.deferredTurnSettlement.put(sessionID, api.SessionIdleDispositionCompleted)
	return nil
}

func (m *Service) Failure(sessionID string, turnErr error) error {
	m.deferredTurnSettlement.put(sessionID, m.Disposition(true))
	return turnErr
}

func (m *Service) Settle(ctx context.Context, sessionID string, disposition api.SessionIdleDisposition) error {
	return m.settleEntry(ctx, sessionID, deferredTurnSettlement{disposition: disposition})
}

func (m *Service) settleEntry(ctx context.Context, sessionID string, entry deferredTurnSettlement) error {
	if m == nil || m.store == nil {
		return nil
	}
	disposition := entry.disposition
	if entry.closeoutBlocked == nil && disposition == api.SessionIdleDispositionCompleted && m.workflows != nil {
		if err := m.workflows.Policy.AssertSessionCompletion(ctx, sessionID); err != nil {
			disposition = m.Disposition(true)
		}
	}
	if err := m.store.SetSessionStatus(ctx, sessionID, api.SessionStatusIdle); err != nil {
		return fmt.Errorf("mark user turn idle: %w", err)
	}
	m.deferredTurnSettlement.remove(sessionID)
	messageOverride := ""
	if entry.closeoutBlocked != nil {
		messageOverride = entry.closeoutBlocked.Notice
	}
	m.status.PublishIdleWithMessage(ctx, sessionID, disposition, messageOverride)
	m.ReconcileSandbox(ctx, sessionID)
	return nil
}

func (m *Service) SettlePending(ctx context.Context, sessionID string) error {
	if m == nil || m.store == nil {
		return nil
	}
	lock := m.prompt.Acquire(sessionID)
	if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	if m.gate.InProgress(ctx, sessionID) {
		m.deferredTurnSettlement.remove(sessionID)
		m.deferredWorkflowCompletions.Delete(sessionID)
		return nil
	}
	completedRun, completed, err := m.completedWorkflow(ctx, sessionID)
	if err != nil {
		return err
	}
	entry, ok := m.deferredTurnSettlement.takeEntry(sessionID)
	hadDeferred := ok
	if completed && !ok {
		entry, ok = deferredTurnSettlement{disposition: api.SessionIdleDispositionCompleted}, true
	}
	if !ok {
		return nil
	}
	loop := m.runtime.CoordinatorLoop()
	release, claimed := loop.Turns.BeginUserTurnSettlement(ctx, sessionID)
	if !claimed {
		if hadDeferred {
			m.deferredTurnSettlement.putEntry(sessionID, entry)
		}
		return nil
	}
	if completed {
		ready, err := loop.Subscriptions.CloseCompletedWorkflowWait(ctx, sessionID)
		if err != nil || !ready {
			release()
			if hadDeferred {
				m.deferredTurnSettlement.putEntry(sessionID, entry)
			}
			return err
		}
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		release()
		if hadDeferred {
			m.deferredTurnSettlement.putEntry(sessionID, entry)
		}
		return fmt.Errorf("read deferred user turn: %w", err)
	}
	if sess.Status != api.SessionStatusBusy {
		if completed {
			m.deferredWorkflowCompletions.CompareAndDelete(sessionID, completedRun)
		}
		release()
		return nil
	}
	err = m.settleEntry(ctx, sessionID, entry)
	if err == nil && completed {
		m.deferredWorkflowCompletions.CompareAndDelete(sessionID, completedRun)
	}
	release()
	if err != nil {
		if hadDeferred {
			m.deferredTurnSettlement.putEntry(sessionID, entry)
		}
	}
	return err
}

// Active workflow phases keep the timer backstop armed.
func (m *Service) DisarmInactiveWorkflow(ctx context.Context, sessionID string) {
	if m == nil || m.workflows == nil {
		return
	}
	if m.workflows.Policy.CurrentPhase(ctx, sessionID) != "" {
		return
	}
	m.runtime.CoordinatorLoop().Waits.DisarmTimerBackstop(ctx, sessionID)
}

// Drain re-enters prompts after releasing the session lock.
func (m *Service) Drain(ctx context.Context, sessionID string) error {
	if m == nil {
		return nil
	}
	hostCtx := context.WithoutCancel(ctx)
	m.runtime.DrainLoopPending(hostCtx, sessionID)
	return m.SettlePending(hostCtx, sessionID)
}

// BeginShutdown marks torn-down turns as interrupted.
func (m *Service) BeginShutdown() {
	if m == nil {
		return
	}
	m.engineStopping.Store(true)
}

func (m *Service) Disposition(promptFailed bool) api.SessionIdleDisposition {
	if !promptFailed {
		return api.SessionIdleDispositionCompleted
	}
	if m != nil && m.engineStopping.Load() {
		return api.SessionIdleDispositionInterrupted
	}
	return api.SessionIdleDispositionTurnError
}
