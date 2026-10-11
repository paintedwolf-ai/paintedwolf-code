package mcp_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPAuthFailureEvictsSessionForFreshReconnect: once a provider's session
// reports 401/403, the next tool call reconnects instead of replaying the stale
// session.
func TestMCPAuthFailureEvictsSessionForFreshReconnect(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools:        map[string][]*sdkmcp.Tool{"svca": {{Name: "query", Description: "query"}}},
		ConnectCount: map[string]int{},
	}
	reg := newTestRegistry(t, conn, "svca")
	if err := reg.Administration.SetProviderEnabled(context.Background(), mcp.CallScope{}, "svca", true, ""); err != nil {
		testutil.FailErr(t, "enable", err)
	}

	// Warm the pool with a healthy session.
	if _, err := reg.Calls.CallTool(context.Background(), mcp.CallScope{}, "svca", "query", nil); err != nil {
		testutil.FailErr(t, "warm call", err)
	}
	if got := conn.ConnectCount["svca"]; got != 1 {
		t.Fatalf("connect count after warm call = %d, want 1", got)
	}

	// Simulate the provider's OAuth bearer expiring mid-session: the transport
	// observes a 401 the way a real remote HTTP MCP server would return one.
	conn.HTTPStatusOnce = map[string]int{"svca": http.StatusUnauthorized}
	if _, err := reg.Calls.CallTool(context.Background(), mcp.CallScope{}, "svca", "query", nil); err == nil {
		t.Fatal("expected the 401 round trip itself to fail")
	}

	// The stale session must not be served again: the very next call has to
	// reconnect rather than repeat the same stale-bearer failure forever.
	if _, err := reg.Calls.CallTool(context.Background(), mcp.CallScope{}, "svca", "query", nil); err != nil {
		testutil.FailErr(t, "call after eviction", err)
	}
	if got := conn.ConnectCount["svca"]; got != 2 {
		t.Fatalf("connect count after eviction = %d, want 2 (fresh session, not the stale pooled one)", got)
	}
}
