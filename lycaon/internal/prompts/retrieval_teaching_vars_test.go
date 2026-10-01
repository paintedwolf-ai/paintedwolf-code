package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/prompts"
)

func retrievalVar(t *testing.T, visible ...string) bool {
	t.Helper()
	vars := prompts.VisibleToolsNativePartialVars(visible)
	got, ok := vars["profile_has_retrieval"].(bool)
	if !ok {
		t.Fatalf("profile_has_retrieval missing or not a bool: %#v", vars["profile_has_retrieval"])
	}
	return got
}

// MCP-only profiles also receive data markers and need their interpretation.
func TestRetrievalVarTrueForEveryRetrievalSurface(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"web search": {"read", ingestion.ToolWebSearch},
		"fetch":      {"read", ingestion.ToolFetchURL},
		"mcp only":   {"read", ingestion.MCPToolPrefix + "acme_deploy"},
		"mixed":      {ingestion.ToolWebSearch, "command", ingestion.MCPToolPrefix + "x"},
	}
	for name, visible := range cases {
		if !retrievalVar(t, visible...) {
			t.Errorf("%s: profile_has_retrieval is false, so this surface meets markers it was never taught", name)
		}
	}
}

func TestRetrievalVarFalseWithoutRetrievalTools(t *testing.T) {
	t.Parallel()
	for name, visible := range map[string][]string{
		"local tools only": {"read", "write", "grep", "command", "git_status"},
		"nothing visible":  nil,
	} {
		if retrievalVar(t, visible...) {
			t.Errorf("%s: profile_has_retrieval is true with no retrieval tool", name)
		}
	}
}
