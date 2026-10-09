package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
)

// AssertSessionNotStuck fails when the session's active run is non-terminal and
// nothing can move it forward: no pending coordinator kick, loop wake, or
// in-flight worker. Such a session waits for the user to type.
func AssertSessionNotStuck(t *testing.T, h *Harness, ctx context.Context, sessionID string) {
	t.Helper()
	if h == nil {
		t.Fatal("AssertSessionNotStuck: nil harness")
	}
	state, err := inspectSessionForwardProgress(ctx, h, sessionID)
	testutil.FailErr(t, "inspectSessionForwardProgress", err)
	if state.terminal {
		return
	}
	if state.hasForwardProgress() {
		return
	}
	if isImplementWorkResting(ctx, h, sessionID, state) {
		return
	}
	t.Fatalf(`session is stuck: run %s in phase %q status %q has no path forward
  pending kick id     = %q
  pending loop wake = %q
  in-flight workers   = %d
A non-terminal run must have one of those, else the only way out is a fresh user prompt.`,
		state.runID, state.phase, state.status,
		state.pendingKickID, state.pendingTrigger, state.inFlightWorkers)
}

// sessionState collects forward-progress signals for one session.
type sessionState struct {
	runID           string
	phase           string
	status          string
	terminal        bool
	pendingKickID   string
	pendingTrigger  string
	inFlightWorkers int
}

func (s sessionState) hasForwardProgress() bool {
	return s.pendingKickID != "" || s.pendingTrigger != "" || s.inFlightWorkers > 0
}

// isImplementWorkResting reports an ambient implement work phase at rest with no pending
// host hooks, the steady state after a build-loop cycle.
func isImplementWorkResting(ctx context.Context, h *Harness, sessionID string, st sessionState) bool {
	if h == nil || h.WorkflowMgr == nil || st.phase != "work" || st.hasForwardProgress() {
		return false
	}
	run, err := h.WorkflowMgr.GetActive(ctx, sessionID)
	if err != nil || run == nil || run.WorkflowID != "implement" {
		return false
	}
	return true
}

func inspectSessionForwardProgress(ctx context.Context, h *Harness, sessionID string) (sessionState, error) {
	st := sessionState{}
	if h.WorkflowMgr != nil {
		run, err := h.WorkflowMgr.GetActive(ctx, sessionID)
		if err != nil {
			return st, err
		}
		if run != nil {
			st.runID = run.ID
			st.phase = run.CurrentPhase
			st.status = string(run.Status)
			st.terminal = workflow.IsTerminal(run.Status)
		}
	}
	if h.SessionMgr != nil {
		if id, ok := h.SessionMgr.Runner.Coordinator.Kicks().PeekPendingKickID(sessionID); ok {
			st.pendingKickID = id
		}
		if trigger, ok := h.SessionMgr.Runner.Coordinator.CoordinatorLoop().PendingForTest(sessionID); ok {
			st.pendingTrigger = string(trigger)
		}
		if sess, err := h.Store.Get(ctx, sessionID); err == nil && sess != nil {
			tasks, err := workeroutcomes.ParentSessionInFlightWorkers(ctx, h.WorkerQueue, sess.ProjectID, sessionID)
			if err == nil {
				st.inFlightWorkers = len(tasks)
			}
		}
	}
	st.phase = strings.TrimSpace(st.phase)
	return st, nil
}
