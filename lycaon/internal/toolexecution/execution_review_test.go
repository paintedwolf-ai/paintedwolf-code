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
	tc := tools.ToolContext{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root", ProcessControl: true, PackageExecution: execution, PolicyWriteGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(filepath.Join(root, "AGENTS.md"))}}
	executor := NewExecutor(nil, nil, "implement")
	action := executor.Capabilities.executionCapabilityAction(t.Context(), "command", map[string]any{"command": "example"}, tc)
	if action.PackageExecution != execution || len(action.AgentPolicy) != 1 {
		t.Fatalf("execution capability dropped independent review facts: %+v", action)
	}
}
