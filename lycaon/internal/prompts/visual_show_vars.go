package prompts

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolpresentation"
)

var (
	visualShowPageTools     = []string{"capture_page", "page_open"}
	visualShowTerminalTools = []string{"command", "terminal_snapshot"}
)

// MergeVisualShowVars derives visual availability from the offered schemas
// and the tools request_tools can still load. Page capture that is only
// loadable sets visual_show_needs_request, not visual_show_page, so the
// prompt asks for the tools without teaching schemas it does not carry. Only
// an agent that can change files builds an interface to capture.
func MergeVisualShowVars(offered, requestable []string, into map[string]any) {
	if into == nil {
		return
	}
	set := toolNameSet(offered)
	loadable := toolNameSet(requestable)
	page := anyPresent(visualShowPageTools, set)
	terminal := anyPresent(visualShowTerminalTools, set)
	needsRequest := !page && set["request_tools"] && anyPresent(visualShowPageTools, loadable) &&
		(hasMutationRole(set) || hasMutationRole(loadable))
	into["visual_show_page"] = page
	into["visual_show_terminal"] = terminal
	into["visual_show_needs_request"] = needsRequest
	into["visual_show_available"] = page || terminal || needsRequest
}

func hasMutationRole(set map[string]bool) bool {
	for name := range set {
		if toolpresentation.Role(name) == "mutation" {
			return true
		}
	}
	return false
}

func toolNameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func mergeVisualShowVarsFromSurface(data AgentToolSurfaceData, into map[string]any) {
	requestable := make([]string, 0, len(data.Requestable))
	for _, row := range data.Requestable {
		requestable = append(requestable, row.Name)
	}
	MergeVisualShowVars(toolViewNames(data.Tools), requestable, into)
}
