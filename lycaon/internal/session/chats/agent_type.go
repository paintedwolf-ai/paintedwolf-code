package chats

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// SetAgentType updates the agent type on a session.
func (m *Service) SetAgentType(ctx context.Context, id, agentType string) error {
	return m.store.UpdateSession(ctx, id, func(s *api.Session) {
		s.AgentType = agentType
	})
}
