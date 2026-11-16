package prompts

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolpresentation"
)

var (
	visualShowPageTools         = []string{"capture_page", "page_open"}
	visualShowTerminalTools     = []string{"command", "terminal_open", "terminal_snapshot"}
	visualShowHeldTerminalTools = []string{"terminal_open", "terminal_snapshot"}
)

// fillVisualShowFacts sets visual capture availability and requestable flags
// from offered and requestable tool sets. Requestable flags require request_tools
// and a mutation-capable agent.
func fillVisualShowFacts(offered, requestable []string, f *ReaderFacts) {
	if f == nil {
		return
	}
	set := toolNameSet(offered)
	loadable := toolNameSet(requestable)
	page := anyPresent(visualShowPageTools, set)
	terminal := anyPresent(visualShowTerminalTools, set)
	canRequest := set["request_tools"] && (hasMutationRole(set) || hasMutationRole(loadable))
	needsRequest := !page && canRequest && anyPresent(visualShowPageTools, loadable)
	terminalNeedsRequest := canRequest && !set["terminal_open"] && allPresent(visualShowHeldTerminalTools, loadable)
	f.VisualShowPage = page
	f.VisualShowTerminal = terminal
	f.VisualShowNeedsRequest = needsRequest
	f.VisualShowTerminalNeedsRequest = terminalNeedsRequest
	f.VisualShowAvailable = page || terminal || needsRequest || terminalNeedsRequest
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
