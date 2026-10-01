package webresearch

const (
	// SearchToolName is the native web_search tool id.
	SearchToolName = "web_search"
	// FetchURLToolName is the native fetch_url tool id.
	FetchURLToolName = "fetch_url"
)

func webResearchToolDisabled(cfg *ConfigStore) bool {
	return cfg != nil && !cfg.SearchEnabled()
}

// SearchToolRuntimeDeny returns a ProfilePolicyEngine runtime deny predicate.
func SearchToolRuntimeDeny(cfg *ConfigStore) func(toolName string) bool {
	return func(toolName string) bool {
		if !webResearchToolDisabled(cfg) {
			return false
		}
		return toolName == SearchToolName || toolName == FetchURLToolName
	}
}

// FilterSearchTool removes web research tools (web_search, fetch_url) from a tool name list.
func FilterSearchTool(names []string) []string {
	if len(names) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == SearchToolName || name == FetchURLToolName {
			continue
		}
		out = append(out, name)
	}
	return out
}
