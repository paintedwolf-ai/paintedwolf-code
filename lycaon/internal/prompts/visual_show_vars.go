package prompts

import "strings"

var (
	visualShowPageTools     = []string{"capture_page", "page_open"}
	visualShowTerminalTools = []string{"command", "terminal_snapshot"}
)

// MergeVisualShowVars derives visual availability from the offered schemas.
func MergeVisualShowVars(offered []string, into map[string]any) {
	if into == nil {
		return
	}
	set := toolNameSet(offered)
	page := anyPresent(visualShowPageTools, set)
	terminal := anyPresent(visualShowTerminalTools, set)
	into["visual_show_page"] = page
	into["visual_show_terminal"] = terminal
	into["visual_show_available"] = page || terminal
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
	MergeVisualShowVars(toolViewNames(data.Tools), into)
}
