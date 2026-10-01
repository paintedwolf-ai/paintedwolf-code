package mcp_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/sandbox"
)

func TestEnabledMCPToolUsesWorkflowToolAccess(t *testing.T) {
	b := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "coordinator",
		Tools: map[string]bool{"read": true},
	}})
	tool := mcp.QualifiedToolName("github", "list_repos")
	if err := b.AssertToolAllowed(context.Background(), "coordinator", tool, sandbox.ToolAccessProfile); err == nil {
		t.Fatal("profile-restricted agent unexpectedly absorbed MCP tool")
	}
	if err := b.AssertToolAllowed(context.Background(), "coordinator", tool, sandbox.ToolAccessAll); err != nil {
		t.Fatalf("open-world agent did not absorb MCP tool: %v", err)
	}
	if !b.ToolDeferred("coordinator", tool, sandbox.ToolAccessAll) {
		t.Fatal("new open-world tools must defer unless the base profile marks them sticky")
	}
}
