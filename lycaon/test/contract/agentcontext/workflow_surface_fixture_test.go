package contract

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func bindFixturePhase(t *testing.T, runCtx api.CoordinatorRunContext) api.CoordinatorRunContext {
	t.Helper()
	manifest := workflowdef.Manifest{
		ID: runCtx.WorkflowID, SurfaceProfile: runCtx.SurfaceProfile,
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: runCtx.CurrentPhase, CoordinatorSurface: runCtx.PhaseCoordinatorSurface,
			SurfaceTemplate: runCtx.PhaseSurfaceTemplate, ModeRefs: runCtx.PhaseModeRefs,
		}},
	}
	binding, err := workflowdef.ResolveSurfaceBinding(manifest, runCtx.CurrentPhase, runCtx.WorkflowID)
	contractcheck.FailErr(t, "resolve fixture phase", err)
	runCtx.PhaseCoordinatorSurface = binding.CoordinatorSurface
	runCtx.PhaseSurfaceTemplate = binding.SurfaceTemplate
	runCtx.PhaseModeRefs = binding.ModeRefs
	if binding.ProfileResolved {
		runCtx.WorkflowInvestigateEligible = new(binding.WorkflowInvestigateEligible)
	}
	return runCtx
}
