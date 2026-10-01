package session

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
)

func mapPromptRunError(err error) error {
	if errors.Is(err, context.Canceled) {
		return lifecycle.ErrStopping
	}
	return err
}

func (m *Manager) attachPromptCancel(parent context.Context, sessionID string) context.Context {
	if m == nil {
		return parent
	}
	ctx, cancel := context.WithCancel(parent)
	// The stop context ignores the turn deadline.
	stop, stopCancel := context.WithCancel(context.WithoutCancel(parent))
	m.promptState.RegisterCancel(sessionID, func() {
		cancel()
		stopCancel()
	})
	// Catch stop beginning before cancellation registration.
	if m.sessionStopInProgress(parent, sessionID) {
		m.CancelInFlightPrompt(sessionID)
	}
	return hitl.WithStopContext(ctx, stop)
}

// detachPromptCancel ends the turn cancel scope and releases the stop context.
func (m *Manager) detachPromptCancel(sessionID string) {
	if m == nil {
		return
	}
	m.promptState.Cancel(sessionID)
}

// CancelInFlightPrompt cancels the prompt without changing session status.
func (m *Manager) CancelInFlightPrompt(sessionID string) {
	if m != nil {
		m.promptState.Cancel(sessionID)
	}
}
