package profiles

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// ResolvePromptToolProfile returns the tool profile Prompt would use for sessionID.
func (m *Service) ResolvePromptToolProfile(ctx context.Context, sessionID string) (string, error) {
	if m == nil || m.store == nil {
		return "", fmt.Errorf("session profile service not configured")
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return m.PromptToolProfile(ctx, sess)
}

// PromptToolProfile uses the workflow manifest, then the agent, defaulting to coordinator.
func (m *Service) PromptToolProfile(ctx context.Context, sess *api.Session) (string, error) {
	coordinatorProfile := ""
	if sess != nil && m.coordinatorProfile != nil {
		coordinatorProfile = m.coordinatorProfile(ctx, sess.ID)
	}
	agents := m.Agents
	if view := m.catalog.ViewForSession(ctx, sess); view != nil {
		agents = view
	}
	return ResolveToolProfile(sess, agents, coordinatorProfile), nil
}
