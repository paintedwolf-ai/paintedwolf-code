package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type sessionWorkerAbort interface {
	AbortAllWorkers(ctx context.Context, sessionID, projectID, reason string) error
	AbortWorkersForRoot(ctx context.Context, projectID, rootID string, projectRoots []projectroot.RootRef, reason string) error
}

type sessionWorkflowStop interface {
	StopSession(ctx context.Context, sessionID, reason string) error
}

type sessionCheckpointStop interface {
	CancelPendingForSession(ctx context.Context, sessionID, reason string) error
}

// SetSessionWorkerAbort wires worker cancellation.
func (m *Manager) SetSessionWorkerAbort(abort sessionWorkerAbort) {
	if m != nil {
		m.sessionWorkerAbort = abort
	}
}

// SetSessionWorkflowStop wires workflow teardown.
func (m *Manager) SetSessionWorkflowStop(stop sessionWorkflowStop) {
	if m != nil {
		m.sessionWorkflowStop = stop
	}
}

// SetSessionCheckpointStop wires checkpoint cancellation.
func (m *Manager) SetSessionCheckpointStop(stop sessionCheckpointStop) {
	if m != nil {
		m.sessionCheckpointStop = stop
	}
}

// Abort stops a session tree and retains its queued turns.
func (m *Manager) Abort(ctx context.Context, id, reason string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("session id required")
	}
	if m == nil || m.store == nil {
		return fmt.Errorf("session manager not configured")
	}
	stopCtx := context.WithoutCancel(ctx)
	if _, err := m.store.Get(stopCtx, id); err != nil {
		return err
	}
	preserveQueueSessionID := ""
	if m.hasAddressedQueuedTurn(stopCtx, id) {
		preserveQueueSessionID = id
	}
	rootID := m.sessionRootID(stopCtx, id)
	flight, leader := m.stopState.Begin(rootID)
	if !leader {
		return flight.Wait(ctx)
	}
	return m.runSessionStopLeader(stopCtx, rootID, flight, reason, sessionStopOptions{preserveQueueSessionID: preserveQueueSessionID})
}

func (m *Manager) hasAddressedQueuedTurn(ctx context.Context, sessionID string) bool {
	if m == nil || m.queue == nil || m.store == nil {
		return false
	}
	for _, item := range m.queue.Snapshot(sessionID).QueueItems {
		itemID := strings.TrimSpace(item.ID)
		if itemID == "" {
			continue
		}
		receipt, err := m.store.GetPromptSubmission(ctx, itemID)
		if err == nil && receipt.SessionID == sessionID && receipt.Status == store.PromptSubmissionQueued {
			return true
		}
	}
	return false
}

type sessionStopOptions struct {
	preserveQueueSessionID        string
	transitionedWorkflowSessionID string
}

// runSessionStopLeader always releases the stop flight, including after a panic.
func (m *Manager) runSessionStopLeader(ctx context.Context, rootID string, flight *lifecycle.Flight, reason string, options sessionStopOptions) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("session stop panicked for root %s: %v", rootID, r)
			slog.ErrorContext(ctx, "session stop panicked",
				"component", "session", "root_session_id", rootID,
				"panic", fmt.Sprintf("%v", r), "stack", string(debug.Stack()))
		}
		m.stopState.Finish(rootID, flight, err)
	}()
	err = m.stopSessionTree(ctx, rootID, reason, options)
	return err
}

func (m *Manager) stopSessionTree(ctx context.Context, rootID, reason string, options sessionStopOptions) error {
	if strings.TrimSpace(reason) == "" {
		reason = "user stopped"
	}
	tree, err := m.store.SessionTreeMembers(ctx, rootID)
	if err != nil {
		return err
	}
	m.cancelTreeTurns(ctx, tree)

	var errs []error
	errs = m.stopTreeWorkers(ctx, tree, reason, errs)

	// Include children admitted before stop began.
	if refreshed, refreshErr := m.store.SessionTreeMembers(ctx, rootID); refreshErr != nil {
		errs = appendStopError(errs, "refresh session tree", rootID, refreshErr)
	} else {
		tree = refreshed
		m.cancelTreeTurns(ctx, tree)
	}

	for _, sess := range tree {
		if m.sessionCheckpointStop != nil {
			errs = appendStopError(errs, "cancel checkpoints", sess.ID,
				m.sessionCheckpointStop.CancelPendingForSession(ctx, sess.ID, reason))
		}
	}
	for _, sess := range tree {
		errs = m.stopOneSessionRuntime(ctx, sess, options.preserveQueueSessionID, errs)
	}
	// Preserve tool-result ordering before workflow boundaries.
	if m.sessionWorkflowStop != nil {
		for _, sess := range tree {
			// The reviewed transition owns this session's final workflow state.
			if sess.ID == options.transitionedWorkflowSessionID {
				continue
			}
			errs = appendStopError(errs, "stop workflows", sess.ID,
				m.sessionWorkflowStop.StopSession(ctx, sess.ID, reason))
		}
	}
	// Catch workers admitted by turns already draining.
	errs = m.stopTreeWorkers(ctx, tree, reason, errs)
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for _, sess := range tree {
		if err := m.store.SetSessionStatus(ctx, sess.ID, api.SessionStatusIdle); err != nil {
			return fmt.Errorf("mark session %s idle: %w", sess.ID, err)
		}
	}
	m.publishSessionIdle(ctx, rootID, idleOutcome{Disposition: api.SessionIdleDispositionUserStopped})
	m.queueChecklistReconcileNudge(ctx, rootID)
	return nil
}

func (m *Manager) cancelTreeTurns(ctx context.Context, tree []store.SessionTreeMember) {
	rt := m.ensureCoordinatorRuntime()
	for _, sess := range tree {
		m.CancelInFlightPrompt(sess.ID)
		rt.CoordinatorLoop().ClearPending(sess.ID)
		rt.Kicks().ClearPending(sess.ID)
		rt.CoordinatorLoop().InterruptSleep(ctx, sess.ID)
	}
}

func (m *Manager) stopTreeWorkers(ctx context.Context, tree []store.SessionTreeMember, reason string, errs []error) []error {
	if m.sessionWorkerAbort == nil {
		return errs
	}
	for _, sess := range tree {
		errs = appendStopError(errs, "abort workers", sess.ID,
			m.sessionWorkerAbort.AbortAllWorkers(ctx, sess.ID, sess.ProjectID, reason))
	}
	return errs
}

// DefaultTurnReleaseTimeout is how long a stop waits for the cancelled turn to
// unwind and release its session before recording the stop without it.
const DefaultTurnReleaseTimeout = 30 * time.Second

func (m *Manager) stopOneSessionRuntime(ctx context.Context, sess store.SessionTreeMember, preserveQueueSessionID string, errs []error) []error {
	defer m.acquireStoppedTurn(ctx, sess.ID)()
	preserveQueue := sess.ID == preserveQueueSessionID
	if m.store != nil {
		if preserveQueue {
			errs = appendStopError(errs, "interrupt running prompt submissions", sess.ID,
				m.store.InterruptRunningPromptSubmissionsBySession(ctx, sess.ID))
		} else {
			errs = appendStopError(errs, "interrupt prompt submissions", sess.ID,
				m.store.InterruptPromptSubmissionsBySession(ctx, sess.ID))
		}
	}
	errs = appendStopError(errs, "release runtime", sess.ID, m.releaseSessionRuntime(ctx, sess.ID))
	if m.queue != nil && !preserveQueue {
		draft := m.queue.CancelAll(sess.ID)
		m.publishQueue(ctx, sess.ID, draft.Revision)
	}
	if interrupter, ok := m.invocations.(invocation.SessionProjectionRecoveryRecorder); ok {
		_, err := interrupter.InterruptRunningSession(ctx, sess.ID)
		errs = appendStopError(errs, "interrupt tool invocations", sess.ID, err)
		if err == nil {
			errs = appendStopError(errs, "reconcile tool results", sess.ID,
				m.recoverInterruptedToolResultPagesForSession(ctx, interrupter, sess.ID))
		}
	}
	return errs
}

// acquireStoppedTurn takes the session's prompt lock from the turn the stop
// already cancelled. A turn still inside a tool after turnReleaseTimeout is
// abandoned: its context is done, so the stop records the interruption and
// settles the session without waiting for it. The returned func releases the
// lock when it was taken.
func (m *Manager) acquireStoppedTurn(ctx context.Context, sessionID string) (release func()) {
	lock := m.promptState.Prompt.Acquire(sessionID)
	timeout := m.turnReleaseTimeout
	if timeout <= 0 {
		timeout = DefaultTurnReleaseTimeout
	}
	var mu sync.Mutex
	abandoned := false
	acquired := make(chan struct{})
	go func() {
		lock.Lock()
		mu.Lock()
		defer mu.Unlock()
		if abandoned {
			lock.Unlock()
			return
		}
		close(acquired)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-acquired:
		return lock.Unlock
	case <-timer.C:
	}
	mu.Lock()
	defer mu.Unlock()
	select {
	case <-acquired:
		return lock.Unlock
	default:
	}
	abandoned = true
	slog.WarnContext(ctx, "cancelled turn did not release its session; stopping without it",
		"component", "session", "session_id", sessionID, "waited", timeout)
	return func() {}
}
