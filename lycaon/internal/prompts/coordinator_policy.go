package prompts

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
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
