package stopping

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type WorkerAbort interface {
	AbortAllWorkers(ctx context.Context, sessionID, projectID, reason string) error
	AbortWorkersForRoot(ctx context.Context, projectID, rootID string, projectRoots []projectroot.RootRef, reason string) error
}

type WorkflowStop interface {
	StopSession(ctx context.Context, sessionID, reason string) error
}

type CheckpointStop interface {
	CancelPendingForSession(ctx context.Context, sessionID, reason string) error
}

// SetWorkflowStop wires workflow teardown.
func (m *Service) SetWorkflowStop(stop WorkflowStop) {
	if m != nil {
		m.workflowStop = stop
	}
}

// SetCheckpointStop wires checkpoint cancellation.
func (m *Service) SetCheckpointStop(stop CheckpointStop) {
	if m != nil {
		m.checkpointStop = stop
	}
}

// Abort stops a session tree and retains its queued turns.
func (m *Service) Abort(ctx context.Context, id, reason string) error {
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
	rootID := m.gate.RootID(stopCtx, id)
	flight, leader := m.gate.Begin(rootID)
	if !leader {
		return flight.Wait(ctx)
	}
	return m.runSessionStopLeader(stopCtx, rootID, flight, reason, sessionStopOptions{preserveQueueSessionID: preserveQueueSessionID})
}

func (m *Service) hasAddressedQueuedTurn(ctx context.Context, sessionID string) bool {
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
func (m *Service) runSessionStopLeader(ctx context.Context, rootID string, flight *lifecycle.Flight, reason string, options sessionStopOptions) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("session stop panicked for root %s: %v", rootID, r)
			slog.ErrorContext(ctx, "session stop panicked",
				"component", "session", "root_session_id", rootID,
				"panic", fmt.Sprintf("%v", r), "stack", string(debug.Stack()))
		}
		m.gate.Finish(rootID, flight, err)
	}()
	err = m.stopSessionTree(ctx, rootID, reason, options)
	return err
}

func (m *Service) stopSessionTree(ctx context.Context, rootID, reason string, options sessionStopOptions) error {
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
		if m.checkpointStop != nil {
			errs = appendStopError(errs, "cancel checkpoints", sess.ID,
				m.checkpointStop.CancelPendingForSession(ctx, sess.ID, reason))
		}
	}
	for _, sess := range tree {
		errs = append(errs, m.StopRuntime(ctx, sess, options.preserveQueueSessionID))
	}
	// Preserve tool-result ordering before workflow boundaries.
	if m.workflowStop != nil {
		for _, sess := range tree {
			// The reviewed transition owns this session's final workflow state.
			if sess.ID == options.transitionedWorkflowSessionID {
				continue
			}
			errs = appendStopError(errs, "stop workflows", sess.ID,
				m.workflowStop.StopSession(ctx, sess.ID, reason))
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
	m.status.PublishIdle(ctx, rootID, api.SessionIdleDispositionUserStopped)
	m.QueueChecklistReconcileNudge(ctx, rootID)
	return nil
}

func (m *Service) cancelTreeTurns(ctx context.Context, tree []store.SessionTreeMember) {
	rt := m.runtime
	for _, sess := range tree {
		m.execution.Cancel(sess.ID)
		if rt == nil {
			continue
		}
		rt.CoordinatorLoop().Nudges.ClearPending(sess.ID)
		rt.Kicks().ClearPending(sess.ID)
		rt.CoordinatorLoop().Waits.InterruptSleep(ctx, sess.ID)
	}
}

func (m *Service) stopTreeWorkers(ctx context.Context, tree []store.SessionTreeMember, reason string, errs []error) []error {
	if m.Workers == nil {
		return errs
	}
	for _, sess := range tree {
		errs = appendStopError(errs, "abort workers", sess.ID,
			m.Workers.AbortAllWorkers(ctx, sess.ID, sess.ProjectID, reason))
	}
	return errs
}

// DefaultTurnReleaseTimeout is how long a stop waits for the cancelled turn to
// unwind and release its session before recording the stop without it.
const DefaultTurnReleaseTimeout = 30 * time.Second

// stoppedTurnPoll is how often a stop re-tries the prompt lock while waiting.
const stoppedTurnPoll = 10 * time.Millisecond

func (m *Service) StopRuntime(ctx context.Context, sess store.SessionTreeMember, preserveQueueSessionID string) error {
	var errs []error
	defer m.AcquireStoppedTurn(ctx, sess.ID)()
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
	errs = appendStopError(errs, "release runtime", sess.ID, m.chats.ReleaseRuntime(ctx, sess.ID))
	if m.queue != nil && !preserveQueue {
		draft := m.queue.CancelAll(sess.ID)
		m.drafts.Publish(ctx, sess.ID, draft.Revision)
	}
	errs = append(errs, m.Recovery.InterruptTools(ctx, sess.ID))
	return errors.Join(errs...)
}

// AcquireStoppedTurn takes the session's prompt lock from the turn the stop
// already cancelled. A turn still inside a tool after turnReleaseTimeout is
// abandoned: its context is done, so the stop records the interruption and
// settles the session without waiting for it. The returned func releases the
// lock when it was taken.
func (m *Service) AcquireStoppedTurn(ctx context.Context, sessionID string) (release func()) {
	timeout := m.turnReleaseTimeout
	if timeout <= 0 {
		timeout = DefaultTurnReleaseTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		lock := m.execution.Prompt.Acquire(sessionID)
		if lock.TryLock() {
			return lock.Unlock
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(stoppedTurnPoll)
	}
	slog.WarnContext(ctx, "cancelled turn did not release its session; stopping without it",
		"component", "session", "session_id", sessionID, "waited", timeout)
	return func() {}
}
