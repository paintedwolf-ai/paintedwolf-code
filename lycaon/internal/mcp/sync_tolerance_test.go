package mcp_test

// SyncTools isolates provider failures and reports them through provider status.

import (
	"context"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/internal/mcp"
)

func TestSyncToolsContinuesWhenOneProviderFails(t *testing.T) {
	// Two synthesized providers; the connector fails on svcb but succeeds on svca.
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{
			"svca": {{Name: "do", Description: "do"}},
			"svcb": nil,
		},
		Err: map[string]error{
			"svcb": errors.New(`calling "initialize": EOF`),
		},
	}
	reg := newTestRegistry(t, conn, "svca", "svcb")

	// Enable each provider one-by-one (SetProviderEnabled calls Load -> SyncTools).
	for _, id := range []string{"svca", "svcb"} {
		if err := reg.SetProviderEnabled(context.Background(), mcp.CallScope{}, id, true, ""); err != nil {
			t.Fatalf("SetProviderEnabled(%s) returned error: %v -- a single broken provider should not fail the call", id, err)
		}
	}

	providers := reg.ListProviders(context.Background(), mcp.CallScope{})
	byID := map[string]struct {
		enabled bool
		lastErr string
	}{}
	for _, s := range providers {
		byID[s.ID] = struct {
			enabled bool
			lastErr string
		}{s.Enabled, s.LastError}
	}
	if !byID["svca"].enabled || byID["svca"].lastErr != "" {
		t.Errorf("svca should be enabled and healthy, got %+v", byID["svca"])
	}
	if !byID["svcb"].enabled {
		t.Errorf("svcb enable flag should be sticky even when sync failed, got %+v", byID["svcb"])
	}
	if byID["svcb"].lastErr != mcp.CodeSyncFailed {
		t.Errorf("svcb last_error = %q want %s", byID["svcb"].lastErr, mcp.CodeSyncFailed)
	}
}

func TestSyncToolsDoesNotPropagateConnectorError(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{"svca": nil},
		Err:   map[string]error{"svca": errors.New("simulated process spawn failure")},
	}
	reg := newTestRegistry(t, conn, "svca")

	if err := reg.SetProviderEnabled(context.Background(), mcp.CallScope{}, "svca", true, ""); err != nil {
		t.Fatalf("SetProviderEnabled returned error: %v -- broken MCP should not crash serve", err)
	}
	// LastSyncError must surface so a future Check call (or UI) can flag it.
	if msg := reg.LastSyncError("svca"); msg == "" {
		t.Fatal("expected non-empty LastSyncError after failed sync")
	}
}
