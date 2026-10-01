package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	workflowReportPhaseRequiredCode = "WORKFLOW_REPORT_PHASE_REQUIRED_BEFORE_CLOSEOUT"
	workflowObligationPendingCode   = "WORKFLOW_OBLIGATION_PENDING"
)

// maybeRejectCloseoutBeforeReportPhase blocks completion outside the report phase.
func (m *Manager) maybeRejectCloseoutBeforeReportPhase(
	ctx context.Context,
	sess *api.Session,
	lastAssistant, surfaceID string,
	invokeAllowed bool,
) (*guidance.Refusal, bool) {
	if m == nil || sess == nil || m.workflows == nil || !invokeAllowed {
		return nil, false
	}
	if !guard.SurfaceFinishesWithUserProse(strings.TrimSpace(surfaceID)) {
		return nil, false
	}
	report, ok := guidance.ParseCoordinatorCompletionReport(lastAssistant)
	if !ok || report.Verification.Valid() && report.Verification.Method == verification.Blocked {
		return nil, false
	}
	state := m.workflows.ActivePhaseGuardState(ctx, sess.ID)
	if !state.ReportCloseoutPending {
		return nil, false
	}
	return m.tryOARFinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.Phase = state.Phase
		gc.HasCompletionReport = true
		gc.WorkflowReportPhasePending = true
		gc.PutRejectData(workflowReportPhaseRequiredCode, map[string]any{"phase": state.Phase})
		return nil
	})
}
