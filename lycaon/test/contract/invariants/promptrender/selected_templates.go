package promptrender

import (
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// runStateContexts are run contexts that make ResolveRunStateOverlay select
// each overlay, built from the typed fields it reads.
var runStateContexts = append(
	[]api.CoordinatorRunContext{{}},
	hoststate.ActiveRunStateContexts(api.CoordinatorRunContext{RunID: "fixture-run"})...,
)

// SelectedTemplateRefs lists, for every catalog surface and shipped phase
// binding, session mode and run state overlay, the surface and mode templates
// the production turn profile selects, so names composed at runtime are
// derived from their selectors.
func SelectedTemplateRefs() ([]string, error) {
	cat, err := surfacecatalog.Load()
	if err != nil {
		return nil, err
	}
	var refs []string
	add := func(ref string) {
		if ref != "" && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	selectAll := func(surfID string, contexts []api.CoordinatorRunContext) error {
		for _, mode := range api.AllSessionModeValues() {
			for _, runCtx := range contexts {
				profile, err := selectProfile(surfID, runCtx, &api.Session{Mode: mode})
				if err != nil {
					return err
				}
				add(profile.SurfaceTemplate)
				for _, modeRef := range profile.ModeRefs {
					add(surface.ModeTemplateRef(modeRef))
				}
			}
		}
		return nil
	}
	for _, surfID := range cat.SurfaceIDs() {
		if err := selectAll(surfID, runStateContexts); err != nil {
			return nil, err
		}
	}
	bindings, err := hoststate.ShippedPhaseBindings()
	if err != nil {
		return nil, err
	}
	for _, b := range bindings {
		bound := b.RunContext()
		if err := selectAll(b.Resolved.CoordinatorSurface, append([]api.CoordinatorRunContext{bound}, hoststate.ActiveRunStateContexts(bound)...)); err != nil {
			return nil, err
		}
	}
	slices.Sort(refs)
	return refs, nil
}

func selectProfile(surfID string, runCtx api.CoordinatorRunContext, sess *api.Session) (profile surface.TurnProfile, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("surface %s: %v", surfID, r)
		}
	}()
	return surface.ResolveTurnProfileForSurface(surfID, runCtx, sess), nil
}

// missingSelectedTemplates names each selected ref no shipped template answers.
func missingSelectedTemplates(s *Stock) ([]string, int, error) {
	refs, err := SelectedTemplateRefs()
	if err != nil {
		return nil, 0, err
	}
	templates, err := interactions.TemplateUnits(s.Catalog)
	if err != nil {
		return nil, 0, err
	}
	var missing []string
	for _, ref := range refs {
		if _, ok := interactions.TemplateIdentity(s.Catalog, templates, ref); !ok {
			missing = append(missing, ref)
		}
	}
	return missing, len(refs), nil
}
