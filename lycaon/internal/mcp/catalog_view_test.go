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
	if !reg.Catalog.ProviderConfigured("fixture") {
		t.Fatal("fixture should be configured")
	}
	if reg.Catalog.ProviderEnabled("fixture") {
		t.Fatal("fixture defaults disabled")
	}
	if !reg.Catalog.ProviderConfigured("other") {
		t.Fatal("other should be configured")
	}
	if reg.Catalog.ProviderEnabled("other") {
		t.Fatal("other defaults disabled until SetProviderEnabled")
	}
	testutil.FailErr(t, "enable other", reg.Administration.SetProviderEnabled(context.Background(), mcp.CallScope{}, "other", true, ""))
	if !reg.Catalog.ProviderEnabled("other") {
		t.Fatal("other should be enabled after SetProviderEnabled")
	}
	if reg.Catalog.ProviderConfigured("missing") || reg.Catalog.ProviderEnabled("missing") {
		t.Fatal("missing must be false")
	}
}

func TestRegistryResolveQualifiedTool(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{"fixture": {{Name: "echo", Description: "echo"}}},
	}
	reg := newTestRegistry(t, conn, "fixture")
	testutil.FailErr(t, "enable", reg.Administration.SetProviderEnabled(context.Background(), mcp.CallScope{}, "fixture", true, ""))
	providerID, toolName, ok := reg.Catalog.ResolveQualifiedTool(mcp.QualifiedToolName("fixture", "echo"))
	if !ok || providerID != "fixture" || toolName != "echo" {
		t.Fatalf("resolve = %q %q %v", providerID, toolName, ok)
	}
	if _, _, ok := reg.Catalog.ResolveQualifiedTool("mcp_missing_tool"); ok {
		t.Fatal("expected miss")
	}
}
