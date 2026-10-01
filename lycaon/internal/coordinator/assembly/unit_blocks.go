package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/pkg/api"
)

// mergeUnitBlocks renders the instruction units this turn carries into the
// `units` slots. The offered set comes from the surface vars already merged;
// omissions come from the turn ledger.
func (e *AssemblyEngine) mergeUnitBlocks(ctx context.Context, pe prompts.PromptTemplateEngine, sess *api.Session, surfaceID string, rootCount int, vars map[string]any) error {
	if e == nil || vars == nil {
		return nil
	}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: strings.TrimSpace(surfaceID)}, rootCount)
	if err != nil {
		return err
	}
	offered, _ := vars["surface_offered"].([]string)
	if offered == nil {
		offered = plan.ImmediateNames()
	}
	catalog, err := prompts.UnitCatalogFor(e.effectiveCatalog(ctx, sess))
	if err != nil {
		return err
	}
	sel := prompts.UnitSelectionVars(promptunit.HostCoordinator, surface.ExecutionModeFamily(surfaceID), plan.ImmediateNames(), offered, e.omittedUnits(sess))
	blocks, err := prompts.RenderUnitSlots(ctx, prompts.UnitRendererFor(pe), catalog, sel, vars)
	if err != nil {
		return err
	}
	prompts.MergeUnitVars(vars, blocks)
	return nil
}

// effectiveCatalog returns the session's resolved catalog, or nil for the
// process catalog.
func (e *AssemblyEngine) effectiveCatalog(ctx context.Context, sess *api.Session) *extpacks.EffectiveCatalog {
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		return view.Catalog
	}
	return nil
}

// omittedUnits returns the instruction units the turn ledger left out.
func (e *AssemblyEngine) omittedUnits(sess *api.Session) map[string]bool {
	if e == nil || sess == nil || e.deps().OmittedUnits == nil {
		return nil
	}
	return e.deps().OmittedUnits(sess.ID)
}
