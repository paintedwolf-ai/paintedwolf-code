package prompts

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// implementDeferUntilUserReplyTools require prose after worker completion.
var implementDeferUntilUserReplyTools = []string{
	"pack_board",
}

// ImplementDeferUntilUserReplyTools returns deferred completion tools.
func ImplementDeferUntilUserReplyTools() []string {
	return append([]string(nil), implementDeferUntilUserReplyTools...)
}

// LaneSyncTool reports an inline survey tool.
func LaneSyncTool(toolName string) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	switch toolName {
	case "read", "grep", "find", "list_dir":
		return true
	default:
		return strings.HasPrefix(toolName, "pack_") || strings.HasPrefix(toolName, "git_")
	}
}

// LaneSyncTools filters visible inline survey tools.
func LaneSyncTools(visibleTools []string) []string {
	var out []string
	for _, name := range visibleTools {
		if LaneSyncTool(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// IsImplementDeferUntilUserReplyTool matches a deferred tool.
func IsImplementDeferUntilUserReplyTool(toolName string) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	for _, blocked := range implementDeferUntilUserReplyTools {
		if toolName == blocked {
			return true
		}
	}
	return false
}

// CoordinatorPolicyTemplateVars builds shared coordinator template data from
// the tools offered on this call and the loadable tools request_tools can add.
func CoordinatorPolicyTemplateVars(offered, requestable []string) map[string]any {
	deferTools := append([]string(nil), implementDeferUntilUserReplyTools...)
	sort.Strings(deferTools)
	laneS := LaneSyncTools(offered)
	out := map[string]any{
		"coordinator_read_line_limit":  readcaps.LineLimit,
		"defer_until_user_reply_tools": deferTools,
		"lane_s_sync_tools":            laneS,
		"visual_review_cadence":        "on_request",
	}
	for k, v := range spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget()) {
		out[k] = v
	}
	for k, v := range progress.ProgressTemplateVars() {
		out[k] = v
	}
	// Guidance follows the offered schemas: a tool that is not on this call
	// is untaught until it loads.
	for k, v := range VisibleToolsNativePartialVars(offered) {
		out[k] = v
	}
	// Coordinator surfaces render for root sessions, which may widen recall;
	// set after the spawn merge so the worker default does not win.
	out["recall_may_widen"] = true
	out["more_tools_loadable"] = len(requestable) > 0
	MergeHTTPActionVars(offered, out)
	return out
}
