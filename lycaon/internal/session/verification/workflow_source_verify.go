package verification

import (
	"context"
	"strings"
)

// WorkflowSourceVerifyPassed evaluates current source evidence for workflow gates.
func (m *Service) WorkflowSourceVerifyPassed(ctx context.Context, sessionID string) (bool, error) {
	if m == nil || m.store == nil || strings.TrimSpace(sessionID) == "" {
		return false, nil
	}
	sess, err := m.store.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || sess == nil {
		return false, err
	}
	history, err := m.store.GetMessages(ctx, sess.ID)
	if err != nil {
		return false, err
	}
	passed, _, _ := m.GateState(ctx, sess, history)
	return passed, nil
}
