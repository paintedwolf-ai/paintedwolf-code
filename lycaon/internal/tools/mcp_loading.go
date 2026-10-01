package tools

// MCPToolPlan is the schema exposure decision for one model call.
type MCPToolPlan struct {
	eager    map[string]bool
	deferred []ToolMeta
}

// Eager reports whether a tool schema belongs on the current model call.
func (p MCPToolPlan) Eager(name string) bool {
	return p.eager[name]
}

// Deferred returns MCP metadata that remains available through request_tools.
func (p MCPToolPlan) Deferred() []ToolMeta {
	out := make([]ToolMeta, len(p.deferred))
	copy(out, p.deferred)
	return out
}

// PlanMCPTools defers automatic MCP tool definitions by default, exposing them eagerly
// only when explicitly configured with always_load or previously activated in the session.
func PlanMCPTools(metas []ToolMeta, activated map[string]bool) MCPToolPlan {
	plan := MCPToolPlan{eager: make(map[string]bool)}
	for _, meta := range metas {
		if !meta.IsMCP() {
			continue
		}
		if meta.AlwaysLoad || activated[meta.Name] {
			plan.eager[meta.Name] = true
			continue
		}
		meta.Deferred = true
		plan.deferred = append(plan.deferred, meta)
	}
	return plan
}
