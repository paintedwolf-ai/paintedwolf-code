package tools

import "testing"

func TestPlanMCPToolsDefersAutomaticSetByDefault(t *testing.T) {
	metas := []ToolMeta{
		{Name: "mcp_alpha_read", Source: ToolSourceMCP, ArgsSchema: map[string]any{"type": "object"}},
		{Name: "mcp_alpha_write", Source: ToolSourceMCP, ArgsSchema: map[string]any{"type": "object"}},
	}
	plan := PlanMCPTools(metas, nil)
	for _, meta := range metas {
		if plan.Eager(meta.Name) {
			t.Fatalf("%s must not be eager by default", meta.Name)
		}
	}
	if got := len(plan.Deferred()); got != 2 {
		t.Fatalf("deferred count = %d, want 2", got)
	}
}

func TestPlanMCPToolsAlwaysAndActivatedAreEager(t *testing.T) {
	metas := []ToolMeta{
		{Name: "mcp_alpha_read", Source: ToolSourceMCP, AlwaysLoad: true},
		{Name: "mcp_beta_read", Source: ToolSourceMCP},
		{Name: "mcp_gamma_read", Source: ToolSourceMCP},
	}
	plan := PlanMCPTools(metas, map[string]bool{"mcp_beta_read": true})
	if !plan.Eager("mcp_alpha_read") {
		t.Fatal("always-loaded tool must be eager")
	}
	if !plan.Eager("mcp_beta_read") {
		t.Fatal("activated tool must be eager")
	}
	if plan.Eager("mcp_gamma_read") {
		t.Fatal("unactivated automatic tool must remain deferred")
	}
	if got := len(plan.Deferred()); got != 1 || plan.Deferred()[0].Name != "mcp_gamma_read" {
		t.Fatalf("unexpected deferred list: %v", plan.Deferred())
	}
}

func TestPlanMCPToolsIgnoresNonMCP(t *testing.T) {
	metas := []ToolMeta{
		{Name: "read"},
		{Name: "mcp_alpha_read", Source: ToolSourceMCP},
	}
	plan := PlanMCPTools(metas, nil)
	if plan.Eager("read") {
		t.Fatal("non-MCP tool must not be registered in MCP plan")
	}
	if len(plan.Deferred()) != 1 || plan.Deferred()[0].Name != "mcp_alpha_read" {
		t.Fatalf("expected only MCP tool deferred, got %v", plan.Deferred())
	}
}
