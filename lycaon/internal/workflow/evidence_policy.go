package workflow

import (
	"context"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

const evidenceLeafPrefix = "evidence_passed:"

// PhaseEvidenceRequirements lists the evidence types a phase declares through
// evidence_passed:<type> gates or complete_when leaves, in declaration order.
func PhaseEvidenceRequirements(manifest workflowdef.Manifest, phaseID string) []string {
	phase, ok := manifest.PhaseByID(phaseID)
	if !ok {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(leaf string) {
		leaf = strings.TrimSpace(leaf)
		if leaf == "closeout_gates_passed" {
			leaf = evidenceLeafPrefix + "verify"
		}
		if !strings.HasPrefix(leaf, evidenceLeafPrefix) {
			return
		}
		kind := strings.TrimSpace(strings.TrimPrefix(leaf, evidenceLeafPrefix))
		if kind == "test" {
			kind = "verify"
		}
		if kind == "" {
			return
		}
		if _, dup := seen[kind]; dup {
			return
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	for _, gate := range phase.Gates {
		add(gate)
	}
	for _, leaf := range decomposeGateExpression(phase.CompleteWhen) {
		add(leaf)
	}
	return out
}

// ActivePhaseRequiresEvidence reports declared evidence gates.
func (m *RunManager) ActivePhaseRequiresEvidence(ctx context.Context, sessionID, evidenceType string) bool {
	if m == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(evidenceType) == "" {
		return false
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return false
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return false
	}
	want := strings.TrimSpace(evidenceType)
	for _, kind := range PhaseEvidenceRequirements(manifest, active.CurrentPhase) {
		if kind == want {
			return true
		}
	}
	return false
}
