package mcp

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMockConnectorAnswersConfiguredResults: a configured result answers its
// provider's tool and no other provider's.
func TestMockConnectorAnswersConfiguredResults(t *testing.T) {
	conn := &MockConnector{Results: map[string]map[string]string{"tracker": {"list_items": `{"choices":[]}`}}}
	session, err := conn.Connect(t.Context(), MCPProviderEntry{ID: "tracker"}, ConnectOpts{})
	testutil.FailErr(t, "connect", err)
	result, err := session.CallTool(t.Context(), "list_items", nil)
	testutil.FailErr(t, "call the configured tool", err)
	if text := result.Content[0].(*sdkmcp.TextContent).Text; text != `{"choices":[]}` {
		t.Fatalf("result = %q", text)
	}
	other, err := conn.Connect(t.Context(), MCPProviderEntry{ID: "other"}, ConnectOpts{})
	testutil.FailErr(t, "connect another provider", err)
	if _, err := other.CallTool(t.Context(), "list_items", nil); err == nil {
		t.Fatal("another provider answered the configured tool")
	}
}
