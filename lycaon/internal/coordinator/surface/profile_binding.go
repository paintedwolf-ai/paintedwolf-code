package surface

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// EnrichRunContextForWorkflow applies the declared surface profile.
func EnrichRunContextForWorkflow(runCtx api.CoordinatorRunContext) api.CoordinatorRunContext {
	if runCtx.HasComposeDraft {
		return runCtx
	}
	if profile := strings.TrimSpace(runCtx.SurfaceProfile); profile != "" {
		return EnrichRunContextWithSurfaceProfile(runCtx, profile)
	}
	wf := strings.ToLower(strings.TrimSpace(runCtx.WorkflowID))
	if wf == "" || wf == "implement" {
		return EnrichRunContextWithSurfaceProfile(runCtx, "implement")
	}
	return runCtx
}

// EnrichRunContextWithSurfaceProfile applies profile-table phase defaults to runCtx.
func EnrichRunContextWithSurfaceProfile(runCtx api.CoordinatorRunContext, profileName string) api.CoordinatorRunContext {
	if profileName == "" && strings.TrimSpace(runCtx.WorkflowID) == "" {
		profileName = "implement"
	}
	if profileName == "" {
		return runCtx
	}
	profiles, err := LoadSurfaceProfiles()
	if err != nil {
		return runCtx
	}
	profile, ok := profiles[profileName]
	if !ok {
		return runCtx
	}
	eligible := profile.InvestigateEligible
	runCtx.WorkflowInvestigateEligible = &eligible
	phaseID := strings.TrimSpace(runCtx.CurrentPhase)
	if binding, ok := profile.Phases[phaseID]; ok {
		runCtx.PhaseCoordinatorSurface = binding.Surface
		runCtx.PhaseSurfaceTemplate = binding.Template
		runCtx.PhaseModeRefs = append([]string(nil), binding.ModeRefs...)
	}
	return runCtx
}
