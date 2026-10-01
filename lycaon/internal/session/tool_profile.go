package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/pkg/api"
)

// AgentProfileResolver resolves tool profiles from agent type.
type AgentProfileResolver interface {
	Get(id string) (agentdef.Profile, error)
}

// ResolveToolProfile uses the workflow manifest, then the session's agent,
// defaulting to coordinator. Posture is not a source: the coordinator surface
// machinery keys on the coordinator profile id, so a posture-selected profile
// would leave the session without phase surfaces.
func ResolveToolProfile(sess *api.Session, agents AgentProfileResolver, coordinatorProfile string) string {
	if cp := strings.TrimSpace(coordinatorProfile); cp != "" {
		return cp
	}
	if sess != nil && sess.AgentType != "" && agents != nil {
		if p, err := agents.Get(sess.AgentType); err == nil && p.ToolProfile != "" {
			return p.ToolProfile
		}
	}
	return "coordinator"
}

// SetAgentType updates the agent type on a session.
func (m *Manager) SetAgentType(ctx context.Context, id, agentType string) error {
	return m.store.UpdateSession(ctx, id, func(s *api.Session) {
		s.AgentType = agentType
	})
}
