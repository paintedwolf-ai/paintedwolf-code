package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestBundledWorkflowsUseShippedCompleteWhenOnly(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "workflows")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read directory entries", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		contractcheck.FailErr(t, "read file", err)
		var doc struct {
			Phases []struct {
				ID           string `yaml:"id"`
				CompleteWhen string `yaml:"complete_when"`
			} `yaml:"phases"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			contractcheck.FailErr(t, "unmarshal YAML document", err)
		}
		for _, p := range doc.Phases {
			cw := strings.TrimSpace(p.CompleteWhen)
			if cw == "" || cw == "gates_satisfied" {
				continue
			}
			if conditions.IsCatalogStub(cw) {
				t.Fatalf("%s phase %q uses catalog stub complete_when %q", e.Name(), p.ID, cw)
			}
		}
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
