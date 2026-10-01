package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// [OAR-PROF-10] Provisional checks cannot substitute for the completed-turn gate.
func (m *Manager) evaluateAgentPostTurn(ctx context.Context, sess *api.Session, content string, workersIdle bool) (*guidance.Refusal, bool) {
	if m.oarPipeline == nil || !m.oarPipeline.AnchorEnforced(oar.AnchorCoordinatorPostTurn) {
		return nil, false
	}
	gc := oar.NewGuardContext()
	m.fillOARSessionFacts(ctx, gc, sess, "", nil)
	gc.LastAssistant = content
	gc.WorkersIdle = workersIdle
	gc.SetContentSegments([]oar.ContentSegment{{Content: content, Role: "assistant", Origin: "model", Authority: "none", TrustTier: "trusted", Source: "agent_turn"}})
	res, err := m.oarPipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorPostTurn, gc)
	if err != nil {
		return guidance.NewRefusal("", err.Error()), true
	}
	reject, blocked, err := m.renderOARResult(ctx, oar.AnchorCoordinatorPostTurn, res)
	if err != nil {
		return guidance.NewRefusal("", err.Error()), true
	}
	return reject, blocked
}
