package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/orchestration"
)

func (m *Manager) maybeNudgeWorkflowPhaseExit(
	ctx context.Context,
	sessionID, profileID string,
	turnTools []string,
) bool {
	if m == nil || m.store == nil || m.coordinatorFrame == nil ||
		strings.TrimSpace(profileID) != orchestration.ProfileCoordinator ||
		turnDefersWorkflowPhaseExit(turnTools) {
		return false
	}
	sess, err := m.store.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || sess == nil {
		return false
	}
	frame, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess)
	if err != nil {
		return false
	}
	wf := workflowEvaluationFromFrame(frame)
	if !wf.CoordinatorPhaseExitRequired() {
		return false
	}
	m.Emit(ctx, sess.ID, anchor.PhaseExitRequired, anchor.Envelope{Vars: map[string]any{
		"workflow_id": wf.WorkflowID,
		"phase_id":    wf.CurrentPhase,
	}})
	return true
}

func turnDefersWorkflowPhaseExit(turnTools []string) bool {
	for _, tool := range turnTools {
		switch strings.TrimSpace(tool) {
		case "ask_user", "workflow_advance", "workflow_transition", "submit_verdict":
			return true
		}
	}
	return false
}
