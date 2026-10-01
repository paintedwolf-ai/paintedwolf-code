package surface

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// CoordinatorCoreTemplate is the bundled coordinator persona.
	CoordinatorCoreTemplate = "agents/coordinator-core.md"
)

// StaticWorkflowHintCodes returns the guidance codes the active-workflow
// inject lists for a bound session's run context.
func StaticWorkflowHintCodes(ctx api.CoordinatorRunContext, hasComposeDraft bool) []string {
	var codes []string
	if len(ctx.FailedLeaves) > 0 {
		codes = append(codes, "WORKFLOW_GATE_UNMET")
	}
	if ctx.PendingFeedback != nil {
		codes = append(codes, "WORKFLOW_FEEDBACK_PENDING")
	}
	if ctx.WorkflowID != "" && !hasComposeDraft && !isKnownWorkflowPhase(ctx) &&
		strings.TrimSpace(ctx.CoordinatorBrief) == "" {
		codes = append(codes, "WORKFLOW_SESSION_COMPOSE_REQUIRED")
	}
	return codes
}

func isKnownWorkflowPhase(ctx api.CoordinatorRunContext) bool {
	if strings.TrimSpace(ctx.PhaseCoordinatorSurface) != "" {
		return true
	}
	return ctx.WorkflowInvestigateEligible != nil && *ctx.WorkflowInvestigateEligible
}

func IsCoordinatorSession(sess *api.Session) bool {
	if sess == nil {
		return false
	}
	return strings.TrimSpace(sess.AgentType) == "coordinator"
}
