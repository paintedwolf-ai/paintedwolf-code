package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// WaitForSessionIdleGate waits until the session can pass an idle gate: the
// coordinator loop has no pending or running host turn for it, the session
// reports idle, and its turn lock is free. A finished prompt may still have
// scheduled a follow-up wake that takes the turn lock asynchronously, so a
// test that rewinds or mutates right after a prompt must wait here first.
func WaitForSessionIdleGate(t *testing.T, h *Harness, ctx context.Context, sessionID string) {
	t.Helper()
	loop := h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop()
	testutil.WaitFor(t, func() bool {
		if loop.Admission.PromptExecutionActive(sessionID) || loop.Nudges.HasPendingLoopWakes(sessionID) || loop.Turns.Active(sessionID) {
			return false
		}
		sess, err := h.Store.Get(ctx, sessionID)
		if err != nil || sess == nil || sess.Status != api.SessionStatusIdle {
			return false
		}
		unlock, free := h.Sessions.Manager.Runner.Execution.TryIdleMutation(sessionID)
		if !free {
			return false
		}
		unlock()
		return true
	})
}
