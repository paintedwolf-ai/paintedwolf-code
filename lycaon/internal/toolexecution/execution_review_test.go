package toolexecution

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"path/filepath"
	"testing"
)

func TestExecutionReviewPreservesPackageAndPolicyFacts(t *testing.T) {
	root := t.TempDir()
	execution := &packageexec.Execution{Manager: "npm"}
	tc := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "root"},
		Execution: tools.InvocationExecution{ProcessControl: true},
		Files: tools.InvocationFiles{PackageExecution: execution,
			PolicyWriteGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(filepath.Join(root, "AGENTS.md"))}},
	}
	executor := NewExecutor(nil, nil, "implement")
	action := executor.Process.executionCapabilityAction(t.Context(), "command", map[string]any{"command": "example"}, tc)
	if action.Execution.PackageExecution != execution || len(action.Mutations.AgentPolicy) != 1 {
		t.Fatalf("execution capability dropped independent review facts: %+v", action)
	}
}
