package testfixture

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolprofiles"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func Boundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{
		{
			ID:    toolprofiles.DefaultToolProfileID,
			Tools: map[string]bool{"read": true, "write": true, "edit": true, "replace_lines": true, "grep": true, "find": true, "stat": true, "wc": true, "list_dir": true, "chmod": true, "delete": true, "survey_repo": true},
		},
		{
			ID:    "coordinator",
			Tools: map[string]bool{"read": true, "write": true, "edit": true, "replace_lines": true, "grep": true, "find": true, "stat": true, "wc": true, "list_dir": true, "chmod": true, "delete": true, "survey_repo": true},
		},
	})
}

func Context(dir string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	// Publish a below-threshold count for open-root tool tests.
	return tools.ToolContext{
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID:        "r1",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
			RepoFileCount:       100,
			RepoFileCountKnown:  true},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID,
			SessionID: "test-session"},
	}
}

func AgentContext(dir, agent string) tools.ToolContext {
	ctx := Context(dir)
	ctx.Identity.Agent = agent
	return ctx
}
