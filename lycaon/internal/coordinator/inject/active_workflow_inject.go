package inject

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoordinatorTurnFrame is one revision-consistent workflow snapshot.
type CoordinatorTurnFrame struct {
	WorkflowRevision int64
	RunContext       api.CoordinatorRunContext
	Runtime          WorkflowRuntimeSnapshot
	// These fields are the turn's policy snapshot.
	ManifestRules    []string
	ScaffoldVars     map[string]any
	PostureRules     []string
	ProjectRootCount int
	OverlayRootPaths []string
	// EvidenceRequirements are the active phase's evidence types.
	EvidenceRequirements []string
	// Roster is the turn's agent-availability snapshot.
	Roster *AgentRoster
	// Machine is the compiled capability state for this turn.
	Machine Machine
}

// RequiresEvidence reports whether the active phase requires an evidence type.
func (f CoordinatorTurnFrame) RequiresEvidence(kind string) bool {
	kind = strings.TrimSpace(kind)
	for _, k := range f.EvidenceRequirements {
		if k == kind {
			return true
		}
	}
	return false
}

// CoordinatorTurnFrameSource loads workflow state for one coordinator turn.
type CoordinatorTurnFrameSource interface {
	BuildCoordinatorTurnFrame(ctx context.Context, sessionID string, sess *api.Session) (CoordinatorTurnFrame, error)
}

// ActiveWorkflowRenderStem returns Binding.render for inject.active_workflow.
func ActiveWorkflowRenderStem(ctx context.Context, sessionID string) string {
	stem, err := anchor.ResolveInformRender(ctx, anchor.InjectActiveWorkflow, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID})
	if err != nil {
		return ""
	}
	return stem
}

// RenderActiveWorkflowInject renders the Binding-resolved active-workflow inject template.
//
//nolint:contextcheck // DTO mapping is pure.
func RenderActiveWorkflowInject(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	sessionID string,
	frame CoordinatorTurnFrame,
	hints *guidance.HintConfig,
	hintCodes []string,
	gateFeedback *feedback.GateFeedbackCatalog,
) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	snap := frame.Runtime
	data := BuildActiveWorkflowInjectData(frame)
	planBody := ""
	if snap.BlueprintBody != "" {
		planBody = snap.BlueprintBody
	}
	data = AttachGateObligations(ctx, data, gateFeedback, frame.RunContext.AdvanceWhenGateMet, planBody)
	block, err := anchor.RenderInform(ctx, anchor.InjectActiveWorkflow, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, ActiveWorkflowInjectToMap(data, hints, hintCodes))
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", nil
	}
	if !strings.Contains(block, ActiveWorkflowInjectSentinel) {
		return "", fmt.Errorf("active-workflow inject missing sentinel %q", ActiveWorkflowInjectSentinel)
	}
	return block, nil
}
