package tools

import (
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// DeferredToolCatalog describes inactive schemas permitted by a compiled model turn.
// A caller without that frozen authority cannot manufacture a discovery catalog.
func DeferredToolCatalog(reg ToolRegistry, plan toolsurface.Plan, active map[string]bool) []turnload.ToolCard {
	return unloadedToolCards(deferredToolMetas(reg, plan), active)
}

func deferredToolMetas(reg ToolRegistry, plan toolsurface.Plan) []ToolMeta {
	if reg == nil || !plan.Compiled() {
		return nil
	}
	var metas []ToolMeta
	for _, meta := range reg.List() {
		if plan.Deferred(meta.Name) {
			metas = append(metas, meta)
		}
	}
	return metas
}

func unloadedToolCards(metas []ToolMeta, active map[string]bool) []turnload.ToolCard {
	cards := make([]turnload.ToolCard, 0, len(metas))
	for _, meta := range metas {
		if !active[meta.Name] {
			cards = append(cards, turnload.ToolCard{Name: meta.Name, Description: meta.Description})
		}
	}
	return cards
}
