package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/pkg/api"
)

type deferredTurnSettlementStore struct {
	mu        sync.Mutex
	bySession map[string]api.SessionIdleDisposition
}

func (s *deferredTurnSettlementStore) put(sessionID string, disposition api.SessionIdleDisposition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bySession == nil {
		s.bySession = make(map[string]api.SessionIdleDisposition)
	}
	s.bySession[sessionID] = disposition
}

func (s *deferredTurnSettlementStore) take(sessionID string) (api.SessionIdleDisposition, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	disposition, ok := s.bySession[sessionID]
	delete(s.bySession, sessionID)
	return disposition, ok
}

func (s *deferredTurnSettlementStore) remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bySession, sessionID)
}

func (m *Manager) beginPromptTurn(sessionID string, pendingKickIDs ...string) {
	if m != nil {
		m.resetCoordinatorBatchTurnGuard(sessionID)
		m.ensureCoordinatorRuntime().BeginPromptTurn(sessionID, pendingKickIDs...)
	}
}

func (m *Manager) endPromptTurn(sessionID string) {
	if m != nil {
		m.ensureCoordinatorRuntime().EndPromptTurn(sessionID)
	}
}

// finishPromptExecution records the next visible-turn transition.
func (m *Manager) finishPromptExecution(ctx context.Context, sessionID string, promptFailed, hostTurn bool, closeoutID string) error {
	if m == nil {
		return nil
	}
	// Host cleanup ignores client cancellation.
	hostCtx := context.WithoutCancel(ctx)
	rt := m.ensureCoordinatorRuntime()
	m.endPromptTurn(sessionID)
	if m.sessionStopInProgress(hostCtx, sessionID) {
		m.deferredTurnSettlement.remove(sessionID)
		rt.CoordinatorLoop().ClearPending(sessionID)
		rt.Kicks().ClearPending(sessionID)
		return nil
	}
	if promptFailed {
		m.deferredTurnSettlement.put(sessionID, m.turnEndDisposition(true))
		return nil
	}
	if m.workflows != nil {
		if err := m.workflows.Phases.ReconcileTurnCompletion(hostCtx, sessionID); err != nil {
			return m.recordUserTurnFailure(sessionID, fmt.Errorf("reconcile workflow turn completion: %w", err))
		}
	}
	continuation := rt.CoordinatorLoop().OnTurnComplete(
		hostCtx, sessionID, hostTurn,
	)
	m.reconcileCoordinatorBatchFromLedger(hostCtx, sessionID)
	m.disarmCoordinatorLoopIfBatchTerminal(hostCtx, sessionID)
	if m.workflows != nil {
		if err := m.workflows.Reports.MaybeDeliverTopologyReport(hostCtx, sessionID, closeoutID); err != nil {
			return m.recordUserTurnFailure(sessionID, fmt.Errorf("deliver topology report: %w", err))
		}
		m.disarmCoordinatorLoopIfNoActiveRun(hostCtx, sessionID)
	}
	if continuation == loopwake.UserTurnContinues {
		m.deferredTurnSettlement.remove(sessionID)
		return nil
	}
	m.deferredTurnSettlement.put(sessionID, api.SessionIdleDispositionCompleted)
	return nil
}

func (m *Manager) recordUserTurnFailure(sessionID string, turnErr error) error {
	m.deferredTurnSettlement.put(sessionID, m.turnEndDisposition(true))
	return turnErr
}

func (m *Manager) settleUserTurn(ctx context.Context, sessionID string, disposition api.SessionIdleDisposition) error {
	if m == nil || m.store == nil {
		return nil
	}
	if err := m.store.SetSessionStatus(ctx, sessionID, api.SessionStatusIdle); err != nil {
		return fmt.Errorf("mark user turn idle: %w", err)
	}
	m.deferredTurnSettlement.remove(sessionID)
	m.publishSessionIdle(ctx, sessionID, idleOutcome{Disposition: disposition})
	m.maybeReconcileSandboxesOnIdle(ctx, sessionID)
	return nil
}

func (m *Manager) publishUserTurnBusy(ctx context.Context, sess *api.Session, preview string, hostTurn, turnWasIdle bool) {
	if m == nil || m.events == nil || sess == nil || (hostTurn && !turnWasIdle) {
		return
	}
	if preview == "" {
		preview = lastAssistantMessageContent(ctx, m.store, sess.ID)
	}
	m.events.PublishSession(ctx, sessionProjectKey(sess), sess.ID, api.SessionStatusBusy, preview)
}

func (m *Manager) settleDeferredUserTurn(ctx context.Context, sessionID string) error {
	if m == nil || m.store == nil {
		return nil
	}
	lock := m.promptState.Prompt.Acquire(sessionID)
	if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	if m.sessionStopInProgress(ctx, sessionID) {
		m.deferredTurnSettlement.remove(sessionID)
		m.deferredWorkflowCompletions.Delete(sessionID)
		return nil
	}
	completedRun, completed, err := m.completedWorkflowSettlement(ctx, sessionID)
	if err != nil {
		return err
	}
	disposition, ok := m.deferredTurnSettlement.take(sessionID)
	hadDeferred := ok
	if completed && !ok {
		disposition, ok = api.SessionIdleDispositionCompleted, true
	}
	if !ok {
		return nil
	}
	loop := m.ensureCoordinatorRuntime().CoordinatorLoop()
	release, claimed := loop.BeginUserTurnSettlement(ctx, sessionID)
	if !claimed {
		if hadDeferred {
			m.deferredTurnSettlement.put(sessionID, disposition)
		}
		return nil
	}
	if completed {
		ready, err := loop.CloseCompletedWorkflowWait(ctx, sessionID)
		if err != nil || !ready {
			release()
			if hadDeferred {
				m.deferredTurnSettlement.put(sessionID, disposition)
			}
			return err
		}
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		release()
		if hadDeferred {
			m.deferredTurnSettlement.put(sessionID, disposition)
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
	err = m.settleUserTurn(ctx, sessionID, disposition)
	if err == nil && completed {
		m.deferredWorkflowCompletions.CompareAndDelete(sessionID, completedRun)
	}
	release()
	if err != nil {
		if hadDeferred {
			m.deferredTurnSettlement.put(sessionID, disposition)
		}
	}
	return err
}

// Active workflow phases keep the timer backstop armed.
func (m *Manager) disarmCoordinatorLoopIfNoActiveRun(ctx context.Context, sessionID string) {
	if m == nil || m.workflows == nil {
		return
	}
	if m.workflows.Policy.CurrentPhase(ctx, sessionID) != "" {
		return
	}
	m.ensureCoordinatorRuntime().CoordinatorLoop().DisarmTimerBackstop(ctx, sessionID)
}

// drainPendingLoopWakes re-enters prompts after releasing the session lock.
func (m *Manager) drainPendingLoopWakes(ctx context.Context, sessionID string) error {
	if m == nil || m.engineStopping.Load() {
		return nil
	}
	hostCtx := context.WithoutCancel(ctx)
	m.ensureCoordinatorRuntime().DrainLoopPending(hostCtx, sessionID)
	return m.settleDeferredUserTurn(hostCtx, sessionID)
}

// idleOutcome is the typed reason carried on the session-idle event.
type idleOutcome struct {
	Disposition api.SessionIdleDisposition
}

// BeginEngineShutdown marks torn-down turns as interrupted.
func (m *Manager) BeginEngineShutdown() {
	if m == nil {
		return
	}
	m.engineStopping.Store(true)
	m.engineWork.Stop()
	m.promptState.Stop()
	m.catalog.Stop()
	m.compactionRunner.Stop()
}

// WaitForEngineShutdown joins turns and detached catalog work before resource release.
func (m *Manager) WaitForEngineShutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	return errors.Join(m.engineWork.Wait(ctx), m.catalog.Wait(ctx), m.compactionRunner.WaitContext(ctx))
}

func (m *Manager) turnEndDisposition(promptFailed bool) api.SessionIdleDisposition {
	if !promptFailed {
		return api.SessionIdleDispositionCompleted
	}
	if m != nil && m.engineStopping.Load() {
		return api.SessionIdleDispositionInterrupted
	}
	return api.SessionIdleDispositionTurnError
}

func (m *Manager) publishSessionIdle(ctx context.Context, sessionID string, outcome idleOutcome) {
	if m == nil || m.events == nil || m.store == nil {
		return
	}
	updated, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return
	}
	lastMessage := lastAssistantMessageContent(ctx, m.store, sessionID)
	m.events.PublishSessionIdle(ctx, sessionProjectKey(updated), sessionID, lastMessage, outcome.Disposition)
}

// lastAssistantMessageContent is the idle preview: one tail read, never the
// transcript. A read failure yields an empty preview rather than a lost event.
func lastAssistantMessageContent(ctx context.Context, store Store, sessionID string) string {
	if store == nil {
		return ""
	}
	content, err := store.LastAssistantMessageContent(ctx, sessionID)
	if err != nil {
		return ""
	}
	return content
}

// lastUserOrAssistantMessageContent returns the latest visible preview.
func lastUserOrAssistantMessageContent(ctx context.Context, store Store, sessionID string) string {
	if store == nil {
		return ""
	}
	content, err := store.LastTurnMessageContent(ctx, sessionID)
	if err != nil {
		return ""
	}
	return content
}
