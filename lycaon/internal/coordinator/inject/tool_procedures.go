package inject

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
)

// RenderToolProceduresBlock renders procedure units for the offered schema set.
func RenderToolProceduresBlock(
	ctx context.Context, renderer *prompts.InjectRenderer,
	sessionID, profileID string, offered []string,
) (string, error) {
	names := append([]string(nil), offered...)
	sort.Strings(names)
	vars := prompts.VisibleToolsNativePartialVars(names)
	vars["offered_tools"] = names
	if !prompts.ScopeAllowsCommandMutation(profileID) {
		prompts.ApplyReadOnlyCommandEffects(names, nil, vars)
	}
	prompts.MergeProfileRequestableVars(profileID, names, vars)
	surface, host := procedureHost(profileID)
	catalog, err := renderer.UnitCatalog()
	if err != nil {
		return "", err
	}
	blocks, err := prompts.RenderUnitSlots(ctx, renderer, catalog, prompts.UnitSelectionVars(host, "", names), vars)
	if err != nil {
		return "", err
	}
	prompts.MergeUnitVars(vars, blocks)
	block, err := anchor.RenderInform(ctx, anchor.InjectToolProcedures,
		anchor.MatchContext{Surface: surface, Profile: profileID, SessionID: sessionID}, renderer, vars)
	return strings.TrimSpace(block), err
}

func procedureHost(profileID string) (string, promptunit.Host) {
	if profileID == prompts.CoordinatorProfileID || prompts.IsCoordinatorSurfaceID(profileID) {
		return "coordinator", promptunit.HostCoordinator
	}
	return "worker", promptunit.HostWorker
}

// ToolProcedureUnits lists the units RenderToolProceduresBlock renders for the
// offered set, in render order.
func ToolProcedureUnits(renderer *prompts.InjectRenderer, profileID string, offered []string) ([]promptunit.Unit, error) {
	catalog, err := renderer.UnitCatalog()
	if err != nil {
		return nil, err
	}
	_, host := procedureHost(profileID)
	rendered := catalog.Rendered(prompts.UnitSelectionVars(host, "", offered))
	var out []promptunit.Unit
	for _, slot := range promptunit.Slots() {
		out = append(out, rendered[slot]...)
	}
	return out, nil
}
