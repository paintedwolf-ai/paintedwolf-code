package toolvocabtest_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
)

func TestExtractToolDescriptionsDistinguishesProvenanceAndWalksSchemas(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "load catalog for consumers", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "load effective tool schemas", err)

	cmdDecl, ok := schemas.ToolMeta("command")
	if !ok {
		t.Fatal("missing command tool schema")
	}
	cmdBase := tools.ToolMeta{
		Name:               "command",
		Description:        cmdDecl.Description,
		DescriptionEffects: cmdDecl.DescriptionEffects,
		ArgsSchema:         cmdDecl.ArgsSchema,
	}

	readOnlyMeta := toolexecution.ProjectCommandMeta(cmdBase, 0, true)
	fullEffects, err := toolcontract.NewCommandEffects(
		toolcontract.CommandMutation,
		toolcontract.CommandExternalExec,
		toolcontract.CommandNetworkEgress,
	)
	testutil.FailErr(t, "build full command effects", err)
	fullMeta := toolexecution.ProjectCommandMeta(cmdBase, fullEffects, false)

	readOnlyHost := toolvocabtest.HostAuthoredToolDescriptions([]tools.ToolMeta{readOnlyMeta})
	if len(readOnlyHost) == 0 {
		t.Fatal("expected projected command descriptions")
	}
	for _, d := range readOnlyHost {
		if d.FieldPath == "capability_request" || strings.HasPrefix(d.FieldPath, "capability_request.") {
			t.Fatalf("read-only projected command schema leaked capability_request description at %q", d.FieldPath)
		}
		if d.FieldPath == "" && strings.Contains(d.Text, "external --") {
			t.Fatalf("externalHidden command description leaked external selector: %q", d.Text)
		}
	}

	fullHost := toolvocabtest.HostAuthoredToolDescriptions([]tools.ToolMeta{fullMeta})
	foundCapReq := false
	foundTopDesc := false
	for _, d := range fullHost {
		if d.FieldPath == "" {
			foundTopDesc = true
		}
		if d.FieldPath == "capability_request" || strings.HasPrefix(d.FieldPath, "capability_request.") {
			foundCapReq = true
		}
	}
	if !foundTopDesc || !foundCapReq {
		t.Fatalf("full command projection missing top description (%v) or capability_request (%v)", foundTopDesc, foundCapReq)
	}

	mcpMeta := tools.ToolMeta{
		Name:              "mcp_github_issue_read",
		Source:            tools.ToolSourceMCP,
		UntrustedMetadata: true,
		Description:       "External MCP description naming `nonexistent_tool`.",
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filters": map[string]any{
					"type":        "array",
					"description": "Filter list.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind": map[string]any{"type": "string", "description": "Nested item kind."},
						},
					},
				},
			},
			"anyOf": []any{
				map[string]any{"description": "First branch."},
			},
		},
	}

	combined := toolvocabtest.ExtractToolDescriptions([]tools.ToolMeta{readOnlyMeta, mcpMeta})
	sawExternalTop := false
	sawNestedItem := false
	sawAnyOf := false
	for _, entry := range combined {
		if entry.Tool == "mcp_github_issue_read" {
			if entry.HostAuthored() {
				t.Fatalf("MCP tool entry marked host-authored: %+v", entry)
			}
			switch entry.FieldPath {
			case "":
				sawExternalTop = true
			case "filters.items.kind":
				sawNestedItem = true
			case "anyOf[0]":
				sawAnyOf = true
			}
		}
	}
	if !sawExternalTop || !sawNestedItem || !sawAnyOf {
		t.Fatalf("missing expected recursive MCP schema descriptions: top=%v nested=%v anyOf=%v", sawExternalTop, sawNestedItem, sawAnyOf)
	}

	hostOnly := toolvocabtest.HostAuthoredToolDescriptions([]tools.ToolMeta{readOnlyMeta, mcpMeta})
	for _, entry := range hostOnly {
		if entry.Tool == "mcp_github_issue_read" || !entry.HostAuthored() {
			t.Fatalf("HostAuthoredToolDescriptions included external entry: %+v", entry)
		}
	}
}
