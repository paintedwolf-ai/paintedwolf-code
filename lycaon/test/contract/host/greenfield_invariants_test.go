package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledWorkflowsUseShippedCompleteWhenOnly(t *testing.T) {
	t.Parallel()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "load bundled workflows", err)
	manifests := reg.List()
	if len(manifests) == 0 {
		t.Fatal("no bundled workflow manifests loaded")
	}
	phases := 0
	for _, m := range manifests {
		for _, p := range m.PhaseDefs {
			phases++
			if conditions.IsCatalogStub(p.CompleteWhen) {
				t.Errorf("%s@%s phase %q uses catalog stub complete_when %q", m.ID, m.Version, p.ID, p.CompleteWhen)
			}
		}
	}
	if phases == 0 {
		t.Fatal("bundled workflow manifests declare no phases")
	}
}

func TestRunManagerDefaultGateEvaluatorFailClosed(t *testing.T) {
	t.Parallel()
	manager := workflow.NewManager(&runstate.Repository{}, nil, nil, nil)
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "work", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"research_satisfied"}}}}
	run := &api.WorkflowRun{CurrentPhase: "work"}
	for name, gate := range map[string]workflowgates.GateEvaluator{"policy": manager.Policy.Gates, "phases": manager.Phases.Gates, "obligations": manager.Obligations.Gates} {
		met, result, err := gate.PhaseGateMet(t.Context(), manifest, run, nil)
		contractcheck.FailErr(t, "evaluate "+name+" default gate", err)
		if met || result.FailedGate != "research_satisfied" {
			t.Fatalf("%s unresolved gate admitted: met=%v result=%+v", name, met, result)
		}
	}
}
