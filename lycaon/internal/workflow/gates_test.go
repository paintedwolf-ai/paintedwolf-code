package workflow

import (
	"context"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseManifestGatesSchema(t *testing.T) {
	m, err := workflowdef.ParseManifestYAML([]byte(`
id: gated
version: 1.0.0
phases:
  - id: verify
    activity_label: Test phase
    gates:
      - evidence_passed:verify
    complete_when: gates_satisfied
`))
	testutil.FailErr(t, "ParseManifestYAML failed", err)
	def, ok := m.PhaseByID("verify")
	if !ok {
		t.Fatal("missing verify phase")
	}
	if len(def.Gates) != 1 || def.Gates[0] != "evidence_passed:verify" {
		t.Fatalf("gates = %v", def.Gates)
	}
	if def.CompleteWhen != "gates_satisfied" {
		t.Fatalf("complete_when = %q", def.CompleteWhen)
	}
}

func TestParseManifestEmptyGateRejected(t *testing.T) {
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: verify
    activity_label: Test phase
    gates:
      - "  "
    complete_when: gates_satisfied
`))
	if err == nil {
		t.Fatal("expected empty gate error")
	}
}

func TestFailClosedGateEvaluatorBlocksUnmetGates(t *testing.T) {
	eval := workflowgates.FailClosedGateEvaluator{}
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "verify", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"human_approval"}},
		},
	}
	run := &api.WorkflowRun{CurrentPhase: "verify"}
	ok, result, err := eval.PhaseGateMet(context.Background(), manifest, run, map[string]any{})
	testutil.FailErr(t, "eval.PhaseGateMet failed", err)
	if ok {
		t.Fatal("expected gate blocked")
	}
	if result.Reason != workflowdef.CompleteWhenGatesSatisfied {
		t.Fatalf("reason = %q", result.Reason)
	}
	vars := runstate.SetGateSatisfied(map[string]any{}, "human_approval", true)
	ok, _, err = eval.PhaseGateMet(context.Background(), manifest, run, vars)
	if err != nil || !ok {
		t.Fatalf("expected gate open: ok=%v err=%v", ok, err)
	}
}
