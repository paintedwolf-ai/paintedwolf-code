package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/vocabulary"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
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
	phases := 0
	for key, manifest := range manifests {
		for _, phase := range manifest.PhaseDefs {
			phases++
			for _, completion := range []string{phase.CompleteWhen, phase.ChildCompleteWhen} {
				if !workflowdef.IsKnownCompleteWhen(completion) {
					t.Errorf("%s phase %s has unknown completion %q", key, phase.ID, completion)
				}
			}
		}
	}
	if phases == 0 {
		t.Fatal("bundled workflow manifests declare no phases")
	}
}

func TestRunManagerDefaultGateEvaluatorFailClosed(t *testing.T) {
	t.Parallel()
	sessions := storetest.New(t)
	manager := workflow.NewManager(workflowpersistence.New(sessions.DB()), sessions, nil, nil)
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
