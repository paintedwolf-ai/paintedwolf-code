package phases

import (
	"context"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"strings"
)

// PhaseEnterHook runs after cross-phase auto-advance.
type PhaseEnterHook func(ctx context.Context, run *RunContext, def workflowdef.PhaseDef)

// PhaseReenterHook runs after same-phase auto-advance.
type PhaseReenterHook func(ctx context.Context, run *RunContext, def workflowdef.PhaseDef)

// RunContext is the active run snapshot passed to phase hooks.
type RunContext struct {
	SessionID       string
	RunID           string
	WorkflowID      string
	WorkflowVersion string
	Phase           string
	PreviousPhase   string // empty on workflow start
}

// WorkflowIdentity returns the workflow ID and version for anchor matching.
func (rc *RunContext) WorkflowIdentity() (string, string) {
	if rc == nil {
		return "", ""
	}
	return rc.WorkflowID, rc.WorkflowVersion
}

// IsRunStart reports the initial phase entry.
func (rc *RunContext) IsRunStart() bool {
	return rc != nil && strings.TrimSpace(rc.PreviousPhase) == ""
}

// ReenterLegForAdvance resolves the same-phase wake leg.
func ReenterLegForAdvance(manifest workflowdef.Manifest, previousPhase, newPhase, sessionID string) (legID string, ok bool) {
	def, ok := manifest.PhaseByID(newPhase)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(previousPhase) != strings.TrimSpace(newPhase) {
		return "", false
	}
	leg := strings.TrimSpace(def.OnReenter.ReenterLeg)
	if leg == "" {
		return "", false
	}
	leg = strings.ReplaceAll(leg, "{session_id}", strings.TrimSpace(sessionID))
	return leg, true
}
