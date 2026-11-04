package prompts

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolpresentation"
)

var (
	visualShowPageTools     = []string{"capture_page", "page_open"}
	visualShowTerminalTools = []string{"command", "terminal_snapshot"}
	// visualShowHeldTerminalTools drive a program that waits for input and
	// capture it at a chosen state.
	visualShowHeldTerminalTools = []string{"terminal_open", "terminal_snapshot"}
)

// MergeVisualShowVars derives visual availability from the offered schemas
// and the tools request_tools can still load. Capture that is only loadable
// sets a needs_request fact, not the offered fact, so the prompt asks for the
// tools without teaching schemas it does not carry: visual_show_needs_request
// for page capture, visual_show_terminal_needs_request for the held terminal.
// Only an agent that can change files builds an interface to capture.
func MergeVisualShowVars(offered, requestable []string, into map[string]any) {
	if into == nil {
		return
	}
	set := toolNameSet(offered)
	loadable := toolNameSet(requestable)
	page := anyPresent(visualShowPageTools, set)
	terminal := anyPresent(visualShowTerminalTools, set)
	canRequest := set["request_tools"] && (hasMutationRole(set) || hasMutationRole(loadable))
	needsRequest := !page && canRequest && anyPresent(visualShowPageTools, loadable)
	terminalNeedsRequest := canRequest && !set["terminal_open"] && allPresent(visualShowHeldTerminalTools, loadable)
	into["visual_show_page"] = page
	into["visual_show_terminal"] = terminal
	into["visual_show_needs_request"] = needsRequest
	into["visual_show_terminal_needs_request"] = terminalNeedsRequest
	into["visual_show_available"] = page || terminal || needsRequest || terminalNeedsRequest
}

func allPresent(names []string, set map[string]bool) bool {
	for _, name := range names {
		if !set[name] {
			return false
		}
	}
	return true
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
	MergeVisualShowVars(toolViewNames(data.Tools), requestableToolNames(data.Requestable), into)
}
