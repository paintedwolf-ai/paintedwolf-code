package runtime

import (
	"context"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// workflowdef.PhaseEvidenceRequirements lists the evidence types a phase declares through
// evidence_passed:<type> gates or complete_when leaves, in declaration order.

// ActivePhaseRequiresEvidence reports declared evidence gates.
func (m *SessionPolicy) ActivePhaseRequiresEvidence(ctx context.Context, sessionID, evidenceType string) bool {
	if m == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(evidenceType) == "" {
		return false
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return false
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return false
	}
	want := strings.TrimSpace(evidenceType)
	for _, kind := range workflowdef.PhaseEvidenceRequirements(manifest, active.CurrentPhase) {
		if kind == want {
			return true
		}
	}
	return false
}
