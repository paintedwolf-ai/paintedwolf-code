package prompts

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/promptunit"
)

// UnitBlocks is the rendered text of every slot, keyed by slot name. Every
// slot is present so templates can place `{{ units.<slot> }}` unconditionally.
type UnitBlocks map[string]string

// UnitRenderer renders one template ref with data; the prompt engine and the
// inject renderer both qualify.
type UnitRenderer interface {
	RenderUnit(ctx context.Context, ref string, data map[string]any) (string, error)
}

// RenderUnitSlots renders the units each slot carries for sel with the same
// variables the host template renders, in catalog order.
func RenderUnitSlots(ctx context.Context, engine UnitRenderer, catalog promptunit.Catalog, sel promptunit.Selection, vars map[string]any) (UnitBlocks, error) {
	out := make(UnitBlocks, len(promptunit.Slots()))
	for _, slot := range promptunit.Slots() {
		out[string(slot)] = ""
	}
	if engine == nil {
		return out, nil
	}
	rendered := catalog.Rendered(sel)
	for slot, units := range rendered {
		var parts []string
		for _, u := range units {
			text, err := engine.RenderUnit(ctx, u.Ref, vars)
			if err != nil {
				return nil, fmt.Errorf("render unit %s: %w", u.ID, err)
			}
			if text = strings.TrimSpace(text); text != "" {
				parts = append(parts, text)
			}
		}
		out[string(slot)] = strings.Join(parts, "\n\n")
	}
	return out, nil
}

// MergeUnitVars publishes rendered slot blocks as `units`.
func MergeUnitVars(into map[string]any, blocks UnitBlocks) {
	if into == nil {
		return
	}
	units := make(map[string]any, len(promptunit.Slots()))
	for _, slot := range promptunit.Slots() {
		units[string(slot)] = blocks[string(slot)]
	}
	into["units"] = units
}

// UnitCatalogFor loads the unit catalog behind an engine's effective catalog,
// or the process catalog when the engine carries none.
func UnitCatalogFor(eff *extpacks.EffectiveCatalog) (promptunit.Catalog, error) {
	if eff == nil {
		resolved, err := extpacks.CatalogForConsumers()
		if err != nil {
			return promptunit.Catalog{}, err
		}
		eff = resolved
	}
	return promptunit.LoadCached(eff)
}

// UnitSelectionVars reads the offered tool names a render already published
// and builds the selection for host. loadable names the tools the render
// could load beyond its floor; floor is what it always offers.
func UnitSelectionVars(host promptunit.Host, mode string, floor, offered []string, omitted map[string]bool) promptunit.Selection {
	return promptunit.Selection{
		Host:    host,
		Mode:    strings.TrimSpace(mode),
		Offered: toolNameSet(offered),
		Floor:   toolNameSet(floor),
		Omitted: omitted,
	}
}

// UnitRendererFor adapts a prompt engine to the unit renderer.
func UnitRendererFor(engine PromptTemplateEngine) UnitRenderer {
	if engine == nil {
		return nil
	}
	if r, ok := engine.(UnitRenderer); ok {
		return r
	}
	return engineUnitRenderer{engine: engine}
}

type engineUnitRenderer struct{ engine PromptTemplateEngine }

func (r engineUnitRenderer) RenderUnit(ctx context.Context, ref string, data map[string]any) (string, error) {
	return r.engine.Render(ctx, ref, data)
}

// UnitCatalogForEngine loads the unit catalog behind a prompt engine's
// effective catalog, or the process catalog when it carries none.
func UnitCatalogForEngine(engine PromptTemplateEngine) (promptunit.Catalog, error) {
	if fe, ok := engine.(*FileTemplateEngine); ok && fe != nil {
		return UnitCatalogFor(fe.Layers().Catalog)
	}
	return UnitCatalogFor(nil)
}
