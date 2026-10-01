package extpacks

import (
	"fmt"
	"sort"
	"strings"
)

// `own:` does not apply to provider-scoped units.
func refuseUnownable(desired DesiredState, kindByID map[string]string) (DesiredState, []Diagnostic) {
	if len(desired.Own) == 0 {
		return desired, nil
	}
	var diags []Diagnostic
	kept := make(map[string]string, len(desired.Own))
	ids := make([]string, 0, len(desired.Own))
	for id := range desired.Own {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		kind := kindByID[id]
		if kind == "" {
			kind = kindRootForRel(id)
		}
		if !ProviderScoped(kind) {
			kept[id] = desired.Own[id]
			continue
		}
		diags = append(diags, Diagnostic{
			Code:   DiagOwnRefused,
			UnitID: id,
			PackID: desired.Own[id],
			Message: fmt.Sprintf(
				"unit %s (%s) does not support own:; %s units come only from their declaring pack — disable the unit instead",
				id, kind, kind),
		})
	}
	if len(kept) == 0 {
		kept = map[string]string{}
	}
	desired.Own = kept
	return desired, diags
}

// applyProjectFloor enforces scope classes before unit resolution.
func applyProjectFloor(
	desired DesiredState,
	provenance DesiredProvenance,
	contributions map[string][]UnitContribution,
	kindByID map[string]string,
) (DesiredState, map[string][]UnitContribution, []Diagnostic) {
	var diags []Diagnostic
	desired, more := filterAdditiveDesired(desired, provenance, contributions, kindByID)
	diags = append(diags, more...)
	return desired, contributions, diags
}

// filterAdditiveDesired protects device-resolved units from project subtraction.
func filterAdditiveDesired(
	desired DesiredState,
	provenance DesiredProvenance,
	contributions map[string][]UnitContribution,
	kindByID map[string]string,
) (DesiredState, []Diagnostic) {
	var diags []Diagnostic
	deviceResolved := func(id string) bool {
		return len(contributions[id]) > 0
	}
	unitKind := func(id string) string {
		if k := kindByID[id]; k != "" {
			return k
		}
		return kindRootForRel(id)
	}

	disabled := make([]string, 0, len(desired.Disabled))
	for _, id := range desired.Disabled {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		origin := OriginDevice
		if provenance.Disabled != nil {
			if o, ok := provenance.Disabled[id]; ok {
				origin = o
			}
		}
		if origin != OriginProject {
			disabled = append(disabled, id)
			continue
		}
		kind := unitKind(id)
		class := ProjectScope(kind)
		if ProjectDisableAllowed(kind, deviceResolved(id)) {
			disabled = append(disabled, id)
			continue
		}
		switch class {
		case ScopeAdditive:
			diags = append(diags, Diagnostic{
				Code:   DiagProjectScopeRefused,
				UnitID: id,
				Message: fmt.Sprintf(
					"unit %s (%s) stays enabled: a project cannot disable a device-resolved %s unit",
					id, kind, kind),
			})
		default:
			diags = append(diags, Diagnostic{
				Code:   DiagProjectScopeRefused,
				UnitID: id,
				Message: fmt.Sprintf(
					"unit %s (%s) disable was not applied from this project: %s units stay on the device catalog",
					id, kind, kind),
			})
		}
	}

	out := desired
	out.Disabled = disabled
	return out, diags
}
