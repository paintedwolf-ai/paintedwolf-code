package inject

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
)

// RenderToolProceduresBlock renders procedure units for the reader's offered schema set.
func RenderToolProceduresBlock(
	ctx context.Context, renderer *prompts.InjectRenderer,
	sessionID string, reader prompts.Reader,
) (string, error) {
	vars := reader.Facts()
	vars["offered_tools"] = reader.Offered()
	surface, profile := procedureTarget(reader)
	catalog, err := renderer.UnitCatalog()
	if err != nil {
		return "", err
	}
	sel := reader.Selection()
	sel.Mode = ""
	blocks, err := prompts.RenderUnitSlots(ctx, renderer, catalog, sel, vars)
	if err != nil {
		return "", err
	}
	prompts.MergeUnitVars(vars, blocks)
	block, err := anchor.RenderInform(ctx, anchor.InjectToolProcedures,
		anchor.MatchContext{Surface: surface, Profile: profile, SessionID: sessionID}, renderer, vars)
	return strings.TrimSpace(block), err
}

func procedureTarget(reader prompts.Reader) (string, string) {
	if reader.Host() == promptunit.HostCoordinator {
		if reader.SurfaceID() != "" {
			return "coordinator", reader.SurfaceID()
		}
		return "coordinator", reader.ProfileID()
	}
	return "worker", reader.ProfileID()
}

// ToolProcedureUnits lists the units RenderToolProceduresBlock renders for the
// reader's offered set, in render order.
func ToolProcedureUnits(renderer *prompts.InjectRenderer, reader prompts.Reader) ([]promptunit.Unit, error) {
	catalog, err := renderer.UnitCatalog()
	if err != nil {
		return nil, err
	}
	sel := reader.Selection()
	sel.Mode = ""
	rendered := catalog.Rendered(sel)
	var out []promptunit.Unit
	for _, slot := range promptunit.Slots() {
		out = append(out, rendered[slot]...)
	}
	return out, nil
}
