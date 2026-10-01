package mcp_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegistryCatalogViewConfiguredEnabled(t *testing.T) {
	reg := newTestRegistry(t, nil, "fixture", "other")
	if !reg.ProviderConfigured("fixture") {
		t.Fatal("fixture should be configured")
	}
	if reg.ProviderEnabled("fixture") {
		t.Fatal("fixture defaults disabled")
	}
	if !reg.ProviderConfigured("other") {
		t.Fatal("other should be configured")
	}
	if reg.ProviderEnabled("other") {
		t.Fatal("other defaults disabled until SetProviderEnabled")
	}
	testutil.FailErr(t, "enable other", reg.SetProviderEnabled(context.Background(), mcp.CallScope{}, "other", true, ""))
	if !reg.ProviderEnabled("other") {
		t.Fatal("other should be enabled after SetProviderEnabled")
	}
	if reg.ProviderConfigured("missing") || reg.ProviderEnabled("missing") {
		t.Fatal("missing must be false")
	}
}

func TestRegistryResolveQualifiedTool(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{"fixture": {{Name: "echo", Description: "echo"}}},
	}
	reg := newTestRegistry(t, conn, "fixture")
	testutil.FailErr(t, "enable", reg.SetProviderEnabled(context.Background(), mcp.CallScope{}, "fixture", true, ""))
	providerID, toolName, ok := reg.ResolveQualifiedTool(mcp.QualifiedToolName("fixture", "echo"))
	if !ok || providerID != "fixture" || toolName != "echo" {
		t.Fatalf("resolve = %q %q %v", providerID, toolName, ok)
	}
	if _, _, ok := reg.ResolveQualifiedTool("mcp_missing_tool"); ok {
		t.Fatal("expected miss")
	}
}
