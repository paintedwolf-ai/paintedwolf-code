package promptloop

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/tools"
)

// addToolDiscovery supplies the same permitted catalog that request_tools pages.
func (l *toolInvocations) addToolDiscovery(sessionID string, tctx tools.ToolContext, data map[string]any) {
	if l.Context.Deps.LoadedTools == nil || tctx.Identity.SessionID == "" || tctx.Identity.SessionID != sessionID || tctx.Identity.Agent == "" {
		return
	}
	cards := tools.DeferredToolCatalog(l.Context.Deps.Tools, tctx.Turn.TurnToolPlan, l.Context.Deps.LoadedTools(sessionID))
	if len(cards) == 0 {
		return
	}
	entries := make([]turnload.DiscoveryEntry, 0, len(cards))
	for _, card := range cards {
		entries = append(entries, turnload.DiscoveryEntry(card))
	}
	page, err := tools.DiscoveryPage(tctx, "request_tools", "available tools", "", "catalog", "", entries)
	if err != nil {
		return
	}
	data["discovery"] = page
	data["tool_discovery_status"] = page.Status
	data["tool_discovery_has_more"] = page.NextNeed != ""
	// Leave room for the invocation and sealing metadata in the 32 KiB provider envelope.
	if raw, err := json.Marshal(data); err != nil || len(raw) > 24<<10 {
		delete(data, "discovery")
		delete(data, "tool_discovery_status")
		delete(data, "tool_discovery_has_more")
	}
}
