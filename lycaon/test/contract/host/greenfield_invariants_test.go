package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/vocabulary"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledWorkflowsUseShippedCompleteWhenOnly(t *testing.T) {
	t.Parallel()
	registry, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "load bundled workflows", err)
	manifests := registry.All()
	if len(manifests) == 0 {
		t.Fatal("empty bundled workflow catalog")
	}
	for key, manifest := range manifests {
		for _, phase := range manifest.PhaseDefs {
			for _, completion := range []string{phase.CompleteWhen, phase.ChildCompleteWhen} {
				if !workflowdef.IsKnownCompleteWhen(completion) {
					t.Errorf("%s phase %s has unknown completion %q", key, phase.ID, completion)
				}
			}
		}
	}
}

func TestRunManagerDefaultGateEvaluatorFailClosed(t *testing.T) {
	t.Parallel()
	manager := workflow.NewManager(&runstate.Repository{}, nil, nil, nil)
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "work", CompleteWhen: vocabulary.CompleteWhenGatesSatisfied, Gates: []string{"research_satisfied"}}}}
	run := &api.WorkflowRun{CurrentPhase: "work"}
	for name, gate := range map[string]workflowgates.GateEvaluator{"policy": manager.Policy.Gates, "phases": manager.Phases.Gates, "obligations": manager.Obligations.Gates} {
		met, result, err := gate.PhaseGateMet(t.Context(), manifest, run, nil)
		contractcheck.FailErr(t, "evaluate "+name+" default gate", err)
		if met || result.FailedGate != "research_satisfied" {
			t.Fatalf("%s unresolved gate admitted: met=%v result=%+v", name, met, result)
		}
	}
}
