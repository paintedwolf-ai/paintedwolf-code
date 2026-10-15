package promptloop

import (
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// addToolDiscovery supplies the same permitted catalog that request_tools pages.
func (l *toolInvocations) addToolDiscovery(sessionID string, tctx tools.ToolContext, data map[string]any) {
	if l.Context == nil || l.Context.Deps.LoadedTools == nil || tctx.Identity.SessionID == "" || tctx.Identity.SessionID != sessionID || tctx.Identity.Agent == "" {
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
	// This page is carried by another tool's refusal, so name its loader explicitly.
	page.Instruction = "Call request_tools with a relevant entry's exact name as need."
	if page.NextNeed != "" {
		page.Instruction += " For more entries, pass next_need unchanged as need."
	}
	data["discovery"] = page
	data["tool_discovery_status"] = page.Status
	data["tool_discovery_has_more"] = page.NextNeed != ""
	// Intrinsic refusal feedback is first, including when escalation appends feedback.
	if !transcript.ToolFeedbackFitsFirst(api.ToolFeedback{Code: "TOOL_NOT_OFFERED", Details: data}) {
		delete(data, "discovery")
		delete(data, "tool_discovery_status")
		delete(data, "tool_discovery_has_more")
	}
}
