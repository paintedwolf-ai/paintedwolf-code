package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestPlanManifestVocabularyRegistered(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	plan, err := manifests.Get("plan", "1.0.0")
	testutil.FailErr(t, "manifests.Get failed", err)
	for _, p := range plan.PhaseDefs {
		if cw := p.CompleteWhen; cw != "" && !workflowdef.IsKnownCompleteWhen(cw) {
			t.Fatalf("phase %q unknown complete_when %q", p.ID, cw)
		}
		if ew := p.EntryWhen; ew != "" && !reg.Has(ew) {
			t.Fatalf("phase %q entry_when %q not registered", p.ID, ew)
		}
	}
	specCfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("spec.yaml"))
	testutil.FailErr(t, "load rules config YAML", err)
	buildCfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("build.yaml"))
	testutil.FailErr(t, "load rules config YAML", err)
	for _, cfg := range []*rules.RulesConfig{specCfg, buildCfg} {
		for _, rule := range cfg.Rules {
			whenStr, err := rules.CanonicalWhenString(rule.When)
			if err != nil {
				t.Fatalf("rule %q: %v", rule.ID, err)
			}
			if _, err := rules.MatchWhenExpr(reg, whenStr, rules.EvalContext{}); err != nil {
				t.Fatalf("rule %q when %q: %v", rule.ID, whenStr, err)
			}
		}
	}
	execute, ok := plan.PhaseByID("execute")
	if !ok || execute.InvokeWorkflow == nil || execute.InvokeWorkflow.WorkflowID != "implement" {
		t.Fatalf("execute invoke_workflow = %+v", execute.InvokeWorkflow)
	}
	if execute.InvokeTrigger != workflowdef.InvokeTriggerPhaseEnter {
		t.Fatalf("execute invoke_trigger = %q want phase_enter", execute.InvokeTrigger)
	}
	for _, g := range execute.Gates {
		if g == "child_run_complete" {
			return
		}
	}
	t.Fatalf("execute gates = %v want child_run_complete", execute.Gates)
}

func TestPlanDomainDoesNotDuplicateCore(t *testing.T) {
	coreReg := conditions.NewRegistry()
	if err := conditions.RegisterCoreConditions(coreReg, conditions.CoreDeps{}); err != nil {
		testutil.FailErr(t, "conditions.RegisterCoreConditions failed", err)
	}
	for _, id := range conditions.ShippedPlanDomainIDs() {
		if coreReg.Has(id) {
			t.Fatalf("plan domain id %q is also registered in core", id)
		}
	}
}
