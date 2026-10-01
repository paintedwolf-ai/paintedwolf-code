package workflowvalidate_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/internal/workflowvalidate"
)

func TestNegativeUnknownAgent(t *testing.T) {
	assertPathsCode(t, `id: bad-agent
version: 1.0.0
agents:
  - { id: totally-fake-agent-id, tools: profile }
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: orchestration_complete
    terminal: true
`, workflowdiag.MustCode("unknown_agent"))
}

func TestNegativeTerminalStamp(t *testing.T) {
	assertPathsCode(t, `id: bad-terminal
version: 1.0.0
agents:
  - { id: implementer, tools: profile }
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: gates_satisfied
    gates: [hitl_consulted:only]
    terminal: true
`, workflowdiag.MustCode("terminal_requires_orchestration_complete"))
}

func TestNegativeMissingAdvanceTool(t *testing.T) {
	// plan_stub surface (from coordinator-surfaces) should not include workflow_advance.
	assertPathsCode(t, `id: bad-advance
version: 1.0.0
agents:
  - { id: implementer, tools: profile }
phases:
  - id: stub
    activity_label: Drafting the stub
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
    complete_when: gates_satisfied
    gates: [hitl_consulted:stub]
    advance:
      when_gate_met: coordinator
    next: done
  - id: done
    activity_label: Done
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`, workflowdiag.MustCode("missing_tool_for_advance_policy"))
}

func assertPathsCode(t *testing.T, yaml string, want workflowdiag.Code) {
	t.Helper()
	root := configlayout.FindModuleRoot()
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(yaml), 0o644))
	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModePaths,
		Paths:      []string{path},
	})
	testutil.FailErr(t, "ValidateCatalog", err)
	for _, d := range diags {
		if d.Code == string(want) {
			return
		}
	}
	for _, d := range diags {
		t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
	}
	t.Fatalf("expected code %s", want)
}
