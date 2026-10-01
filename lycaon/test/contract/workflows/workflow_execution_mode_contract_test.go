package contract

import (
	"strings"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowManifestExecutionModeFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	m, err := workflowdef.ParseManifestYAML([]byte(`
id: execution-mode-contract
version: 1.0.0
controls:
  default_execution_mode: investigate
phases:
  - id: inspect
    activity_label: Inspecting
    on_enter:
      set_execution_mode: investigate
    next: work
  - id: work
    activity_label: Working
    on_enter:
      set_execution_mode: orchestrate
    terminal: true
    complete_when: orchestration_complete
`))
	contractcheck.FailErr(t, "ParseManifestYAML execution-mode contract", err)
	if m.Controls.DefaultExecutionMode != workflowdef.ExecutionModeInvestigate {
		t.Fatalf("default = %q", m.Controls.DefaultExecutionMode)
	}
	inspect, ok := m.PhaseByID("inspect")
	if !ok || inspect.OnEnter.SetExecutionMode != workflowdef.ExecutionModeInvestigate {
		t.Fatalf("inspect on_enter = %+v", inspect.OnEnter)
	}
	work, ok := m.PhaseByID("work")
	if !ok || work.OnEnter.SetExecutionMode != workflowdef.ExecutionModeOrchestrate {
		t.Fatalf("work on_enter = %+v", work.OnEnter)
	}
}

func TestWorkflowManifestRejectsInvalidExecutionMode(t *testing.T) {
	t.Parallel()
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
controls:
  default_execution_mode: parallel
phases:
  - id: boot
    activity_label: Starting
`))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "default_execution_mode") {
		t.Fatalf("err = %v", err)
	}
}
