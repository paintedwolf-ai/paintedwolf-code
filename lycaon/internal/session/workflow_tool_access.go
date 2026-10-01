package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowToolAccessView resolves a workflow's explicit tool policy for one agent.
type WorkflowToolAccessView interface {
	AgentToolAccess(ctx context.Context, sessionID, agentType string) sandbox.ToolAccess
}

// SetWorkflowToolAccessView wires workflow-declared open-world tool access.
func (m *Manager) SetWorkflowToolAccessView(v WorkflowToolAccessView) {
	if m != nil {
		m.workflowToolAccess = v
	}
}

// ResolveToolAccess reports the workflow-declared tool breadth for a session.
func (m *Manager) ResolveToolAccess(ctx context.Context, sess *api.Session) sandbox.ToolAccess {
	if m == nil || sess == nil || m.workflowToolAccess == nil {
		return sandbox.ToolAccessProfile
	}
	sessionID := strings.TrimSpace(sess.ID)
	if parentID := strings.TrimSpace(sess.ParentSessionID); parentID != "" {
		sessionID = parentID
	}
	agentType := strings.TrimSpace(sess.AgentType)
	if agentType == "" {
		agentType = orchestration.ProfileCoordinator
	}
	access := m.workflowToolAccess.AgentToolAccess(ctx, sessionID, agentType)
	if access == sandbox.ToolAccessAll {
		return access
	}
	return sandbox.ToolAccessProfile
}
