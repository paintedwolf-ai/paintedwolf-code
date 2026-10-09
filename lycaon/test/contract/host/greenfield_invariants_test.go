package contract

import (
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeWiresConditionRegistry(t *testing.T) {
	t.Parallel()
	text := contractcheck.ServeWireSource(t)
	for _, required := range []string{
		"SetConditionRegistry",
		"DelegationCloseout:",
		"ValidateBundled",
		"ValidateServeWiring",
		"NewPostureRuleEngine",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("serve wire missing production wiring %q", required)
		}
	}
}

func TestSessionCreationWarmsPostureOverlay(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "api", "sessionadmin", "session_create.go"))
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "WarmPostureOverlay") {
		t.Fatal("session_create.go must warm posture overlay during session materialization")
	}
}

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

func TestNoDiskPlanReadInConditions(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	condDir := filepath.Join(root, "lycaon", "internal", "conditions")
	var hits []string
	err := filepath.Walk(condDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, forbidden := range []string{"ReadMarkdownFromProject", "blueprintfile.Read"} {
			if strings.Contains(text, forbidden) {
				hits = append(hits, path+": "+forbidden)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "operation failed", err)
	if len(hits) > 0 {
		t.Fatalf("conditions must not read plan markdown from disk:\n%s", strings.Join(hits, "\n"))
	}
}

func TestRegistryGateEvaluatorNoDiskPlanRead(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "workflow", "registry_gate.go"))
	contractcheck.FailErr(t, "read file", err)
	text := string(data)
	for _, forbidden := range []string{"ReadMarkdownFromProject", "blueprintfile.Read"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("registry_gate.go must not use disk plan reads (%s)", forbidden)
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
