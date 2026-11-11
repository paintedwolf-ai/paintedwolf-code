package prompts

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// LaneSyncTool reports whether toolName is an inline survey tool or operation.
func LaneSyncTool(toolName string) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	if toolName == "command" || strings.HasPrefix(toolName, "pack_") {
		return true
	}
	contract, ok := toolcontract.Lookup(toolName)
	if !ok || contract.Lifecycle != toolcontract.LifecycleReadOnly {
		return false
	}
	return contract.Owner == "filesystem" || contract.Owner == "git"
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

// CoordinatorPolicyTemplateVars builds shared coordinator template data from
// the tools offered on this call and the loadable tools request_tools can add.
func CoordinatorPolicyTemplateVars(offered, requestable []string) map[string]any {
	laneS := LaneSyncTools(offered)
	out := map[string]any{
		"coordinator_read_line_limit": readcaps.LineLimit,
		"lane_s_sync_tools":           laneS,
		"visual_review_cadence":       "on_request",
	}
	for k, v := range spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget()) {
		out[k] = v
	}
	for k, v := range progress.ProgressTemplateVars() {
		out[k] = v
	}
	for k, v := range VisibleToolsNativePartialVars(offered) {
		out[k] = v
	}
	out["recall_may_widen"] = true
	out["is_coordinator"] = true
	out["more_tools_loadable"] = len(requestable) > 0
	out["profile_can_request"] = requestableToolSet(offered, requestable)
	available := append(append([]string(nil), offered...), requestable...)
	out["profile_mutation_capable"] = ToolNamesMutationCapable(available)
	MergeHTTPActionVars(offered, out)
	return out
}
