package verification

import (
	"context"
	"strings"
)

// WorkflowSourceVerifyPassed evaluates current source evidence for workflow gates.
func (m *Service) WorkflowSourceVerifyPassed(ctx context.Context, sessionID string) (bool, error) {
	if m == nil || (m.sessionStore == nil || m.transcriptStore == nil) || strings.TrimSpace(sessionID) == "" {
		return false, nil
	}
	sess, err := m.sessionStore.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || sess == nil {
		return false, err
	}
	history, err := m.transcriptStore.GetMessages(ctx, sess.ID)
	if err != nil {
		return false, err
	}
	passed, _, _ := m.GateState(ctx, sess, history)
	return passed, nil
}
