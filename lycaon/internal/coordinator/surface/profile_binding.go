package surface

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// EnrichRunContextForWorkflow applies the declared surface profile.
func EnrichRunContextForWorkflow(runCtx api.CoordinatorRunContext, configRoot string) api.CoordinatorRunContext {
	if runCtx.HasComposeDraft {
		return runCtx
	}
	if profile := strings.TrimSpace(runCtx.SurfaceProfile); profile != "" {
		return EnrichRunContextWithSurfaceProfile(runCtx, profile, configRoot)
	}
	wf := strings.ToLower(strings.TrimSpace(runCtx.WorkflowID))
	if wf == "" || wf == "implement" {
		return EnrichRunContextWithSurfaceProfile(runCtx, "implement", configRoot)
	}
	return runCtx
}
