package oar

import (
	"strings"
	"testing"
)

var structuralMCPFacts = []string{
	"mcp_provider_id",
	"mcp_tool_name",
	"mcp_qualified_tool",
	"mcp_provider_configured",
	"mcp_provider_enabled",
	"mcp_call_ok",
	"mcp_error_code",
	"mcp_schema_matched",
}

func TestMCPCatalogueContainsStructuralFacts(t *testing.T) {
	for _, name := range structuralMCPFacts {
		if _, ok := catalogueFacts[name]; !ok {
			t.Fatalf("catalogueFacts missing %q", name)
		}
	}
}

func TestMCPCatalogueConditionParity(t *testing.T) {
	act := activation(NewGuardContext())
	for _, name := range structuralMCPFacts {
		if _, ok := act[name]; !ok {
			t.Fatalf("activation missing %q", name)
		}
	}
	for _, name := range structuralMCPFacts {
		var expr string
		switch name {
		case "mcp_provider_id", "mcp_tool_name", "mcp_qualified_tool", "mcp_error_code":
			expr = name + ` == ""`
		default:
			expr = "!" + name
		}
		if err := checkWhenAgainstSpec(expr, nil); err != nil {
			t.Fatalf("condition catalogue missing %q: %v", name, err)
		}
	}
	if err := checkWhenAgainstSpec(`mcp_provider_configured_for("x")`, nil); err != nil {
		t.Fatalf("mcp_provider_configured fn: %v", err)
	}
	if err := checkWhenAgainstSpec(`mcp_provider_enabled_for("x")`, nil); err != nil {
		t.Fatalf("mcp_provider_enabled fn: %v", err)
	}
	for _, expr := range []string{
		`mcp_has_field("k")`,
		`mcp_field_bool("k")`,
		`mcp_field_string("k") == ""`,
		`mcp_field_int("k") == 0`,
	} {
		if err := checkWhenAgainstSpec(expr, nil); err != nil {
			t.Fatalf("condition accessor %q: %v", expr, err)
		}
	}
}

func TestMCPCatalogueNoVendorPrefixes(t *testing.T) {
	for name := range catalogueFacts {
		for _, prefix := range []string{"linear_", "github_", "slack_"} {
			if strings.HasPrefix(name, prefix) {
				t.Fatalf("vendor-prefixed catalogue fact %q", name)
			}
		}
	}
}
