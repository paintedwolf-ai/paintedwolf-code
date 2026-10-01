package tools

// SchemaActivation is the per-session set of loaded schemas: request_tools
// writes it and the prompt loop offers what it holds. The coordinator's turn
// load ledger implements it.
type SchemaActivation interface {
	// Active returns the loaded tool names for sessionID, or nil.
	Active(sessionID string) map[string]bool
	// Activate records names as loaded for sessionID; need is the text
	// request_tools resolved, kept for the receipt.
	Activate(sessionID string, names []string, need string)
}

// HideSkillsReadWhenEmpty drops skills_read when the compiled skill catalog is empty.
func HideSkillsReadWhenEmpty(metas []ToolMeta, skillCount int) []ToolMeta {
	if skillCount > 0 {
		return metas
	}
	out := make([]ToolMeta, 0, len(metas))
	for _, meta := range metas {
		if meta.Name == "skills_read" {
			continue
		}
		out = append(out, meta)
	}
	return out
}

// FilterDeferredMetas drops deferred tools that the session has not activated.
func FilterDeferredMetas(metas []ToolMeta, activated map[string]bool) []ToolMeta {
	out := make([]ToolMeta, 0, len(metas))
	for _, meta := range metas {
		if meta.Deferred && !activated[meta.Name] {
			continue
		}
		out = append(out, meta)
	}
	return out
}
