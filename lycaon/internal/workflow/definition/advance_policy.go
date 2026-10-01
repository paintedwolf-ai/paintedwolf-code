package definition

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// AdvanceWhenGateMet selects who advances when phase gates are satisfied.
type AdvanceWhenGateMet string

const (
	AdvanceWhenGateMetAuto        AdvanceWhenGateMet = "auto"
	AdvanceWhenGateMetCoordinator AdvanceWhenGateMet = "coordinator"
)

// LoopExit selects the target phase on advance.
type LoopExit string

const (
	LoopExitNext LoopExit = "next"
	LoopExitSelf LoopExit = "self"
)

// EffectiveAdvancePolicy resolves per-phase advance trigger.
func EffectiveAdvancePolicy(m Manifest, phase PhaseDef) AdvanceWhenGateMet {
	if phase.AdvanceWhenGateMet != "" {
		return phase.AdvanceWhenGateMet
	}
	if m.Controls.PhaseAdvance.HostOnly() {
		return AdvanceWhenGateMetAuto
	}
	return AdvanceWhenGateMetCoordinator
}

// EffectiveLoopExit returns loop.exit for a phase (default next).
func EffectiveLoopExit(phase PhaseDef) LoopExit {
	if phase.LoopExit != "" {
		return phase.LoopExit
	}
	return LoopExitNext
}

// PhaseHostPhaseAdvance reports whether the host auto-advances the given phase
// (workflow_advance stays off the coordinator surface when true).
func PhaseHostPhaseAdvance(m Manifest, phaseID string) bool {
	def, ok := m.PhaseByID(phaseID)
	if !ok {
		return m.Controls.PhaseAdvance.HostOnly()
	}
	return EffectiveAdvancePolicy(m, def) == AdvanceWhenGateMetAuto
}

// ResolveAdvanceTarget picks the next phase id after a successful advance.
func (m Manifest) ResolveAdvanceTarget(current string) (string, bool) {
	def, ok := m.PhaseByID(current)
	if !ok {
		return m.NextPhase(current)
	}
	if EffectiveLoopExit(def) == LoopExitSelf {
		return def.ID, true
	}
	return m.NextPhase(current)
}

// ResolveAdvanceTargetForRun applies the manifest's explicit child-only edge
// when this run is an invoked subroutine. Root runs retain their ordinary loop.
func (m Manifest) ResolveAdvanceTargetForRun(run *api.WorkflowRun, current string) (string, bool) {
	def, ok := m.PhaseByID(current)
	if ok && RunHasParent(run) {
		if target := strings.TrimSpace(def.ChildNext); target != "" {
			return target, true
		}
	}
	return m.ResolveAdvanceTarget(current)
}

func parseAdvanceWhenGateMet(phaseID, raw string) (AdvanceWhenGateMet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	switch AdvanceWhenGateMet(raw) {
	case AdvanceWhenGateMetAuto, AdvanceWhenGateMetCoordinator:
		return AdvanceWhenGateMet(raw), nil
	default:
		return "", phaseFieldEnumError(phaseID, "advance.when_gate_met", raw, "auto", "coordinator")
	}
}

func parseLoopExit(phaseID, raw string) (LoopExit, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	switch LoopExit(raw) {
	case LoopExitNext, LoopExitSelf:
		return LoopExit(raw), nil
	default:
		return "", phaseFieldEnumError(phaseID, "loop.exit", raw, "next", "self")
	}
}

func phaseFieldEnumError(phaseID, field, got string, allowed ...string) error {
	return fmt.Errorf("phase %q: invalid %s %q (want %s)", phaseID, field, got, strings.Join(allowed, " | "))
}

func PhaseHasGate(def PhaseDef, gate string) bool {
	gate = strings.TrimSpace(gate)
	for _, g := range def.Gates {
		if strings.TrimSpace(g) == gate {
			return true
		}
	}
	return false
}

func RunHasParent(run *api.WorkflowRun) bool {
	return run != nil && run.ParentRunID != nil && strings.TrimSpace(*run.ParentRunID) != ""
}
