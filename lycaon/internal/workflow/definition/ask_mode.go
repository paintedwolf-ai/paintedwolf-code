package definition

import (
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// EntryRequiresAgent classifies the same entry used by workflow admission.
func (m Manifest) EntryRequiresAgent() bool {
	return SupportsBlueprints(m) || m.PhaseRequiresAgent(m.FirstPhase())
}

// PhaseRequiresAgent identifies required effects before phase entry runs hooks.
func (m Manifest) PhaseRequiresAgent(phaseID string) bool {
	def, ok := m.PhaseByID(phaseID)
	if !ok {
		return true
	}
	if def.BlueprintWrite || def.ReviewLoop != nil || def.HumanApproval != nil ||
		def.ContentReview != nil || len(def.TouchPaths) > 0 || def.InvokeWorkflow != nil || def.MayHostHold() ||
		def.ParallelTask != nil && def.ParallelTask.MaxWriteWorkers > 0 ||
		api.SessionPosture(def.OnEnter.SetPosture) == api.SessionPostureVet ||
		api.SessionPosture(def.OnEnter.SetPosture) == api.SessionPostureSpec {
		return true
	}
	requiredEvidence := PhaseEvidenceRequirements(m, phaseID)
	if slices.Contains(requiredEvidence, string(evidence.GateTypeVerify)) ||
		slices.Contains(requiredEvidence, string(evidence.GateTypeTest)) ||
		def.CompleteWhen == "closeout_gates_passed" {
		return true
	}
	binding, err := ResolveSurfaceBinding(m, phaseID, m.ID, "")
	if err != nil {
		return true
	}
	if binding.CoordinatorSurface == "" {
		return false
	}
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return true
	}
	row, err := catalog.Surface(binding.CoordinatorSurface)
	return err != nil || row.RequiresAgent || row.Exit == "verdict"
}
