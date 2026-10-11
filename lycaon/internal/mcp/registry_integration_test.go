//go:build integration

package mcp_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPSpawnAndListTools(t *testing.T) {
	conn := &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{
		"svca": {{Name: "do", Description: "do"}},
	}}
	reg := newTestRegistry(t, conn, "svca")
	if err := reg.Administration.SetProviderEnabled(context.Background(), mcp.CallScope{}, "svca", true, ""); err != nil {
		testutil.FailErr(t, "reg.Administration.SetProviderEnabled failed", err)
	}
	tools, err := reg.Calls.ListTools(context.Background(), mcp.CallScope{}, "svca")
	testutil.FailErr(t, "reg.Calls.ListTools failed", err)
	if len(tools) == 0 {
		t.Fatal("expected tools")
	}
	if tools[0].Name != "mcp_svca_do" {
		t.Fatalf("tool name = %q", tools[0].Name)
	}
}
