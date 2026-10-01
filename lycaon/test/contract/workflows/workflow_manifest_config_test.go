package contract

import (
	"path/filepath"
	"strings"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowManifestConfigPlanShape(t *testing.T) {
	t.Parallel()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	catalog := reg.All()
	plan, ok := catalog["plan@1.0.0"]
	if !ok {
		t.Fatal("missing plan@1.0.0")
	}
	resolved := plan
	want := []string{"research", "expand", "approve", "execute", "done"}
	if len(resolved.Phases) != len(want) {
		t.Fatalf("phases = %v", resolved.Phases)
	}
	for i, p := range want {
		if resolved.Phases[i] != p {
			t.Fatalf("phase[%d] = %q want %q", i, resolved.Phases[i], p)
		}
	}
	if _, ok := resolved.Parameters["auto_approve"]; !ok {
		t.Fatal("plan must keep auto_approve parameter for plan@+params starts")
	}
	if _, ok := resolved.Parameters["research_depth"]; !ok {
		t.Fatal("plan must keep research_depth parameter")
	}
}

func TestWorkflowManifestForbiddenOnEnterMode(t *testing.T) {
	t.Parallel()
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: research
    on_enter:
      set_posture: research
`))
	if err == nil {
		t.Fatal("expected load failure for research session posture")
	}
	if !strings.Contains(err.Error(), "research") {
		t.Fatalf("err = %v", err)
	}
}

func TestBundledPlanExecutionSubroutine(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "plan", "workflows", "plan", "workflow.yaml")
	m, err := workflowdef.LoadManifestFromFile(path)
	contractcheck.FailErr(t, "workflow.LoadManifestFromFile failed", err)
	execute, ok := m.PhaseByID("execute")
	if !ok {
		t.Fatal("missing execute phase")
	}
	if execute.InvokeWorkflow == nil || execute.InvokeWorkflow.WorkflowID != "implement" {
		t.Fatalf("execute invoke_workflow = %+v", execute.InvokeWorkflow)
	}
	if execute.OnEnter.SetPosture != "" {
		t.Fatalf("execute set_posture = %q, want empty", execute.OnEnter.SetPosture)
	}
}
