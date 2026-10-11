//go:build integration

package definition_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestBundledPlanManifestLoadValidation(t *testing.T) {
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	ruleConfigs, err := rules.LoadBundledRuleConfigs()
	testutil.FailErr(t, "rules.LoadBundledRuleConfigs failed", err)
	if diags := vocabulary.ValidateBundled(reg, manifests, ruleConfigs); len(diags) > 0 {
		t.Fatalf("vocabulary.ValidateBundled failed: %v", diags)
	}
	plan, err := manifests.Get("plan", "1.0.0")
	testutil.FailErr(t, "manifests.Get failed", err)
	if plan.Request == nil || plan.Request.Question != "What would you like to plan?" || plan.Request.Default != "" {
		t.Fatalf("plan request = %+v", plan.Request)
	}
	if plan.Phases[0] != "research" {
		t.Fatalf("plan first phase = %q want research", plan.Phases[0])
	}
}

func TestBundledOptionsManifestLoadValidation(t *testing.T) {
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	ruleConfigs, err := rules.LoadBundledRuleConfigs()
	testutil.FailErr(t, "rules.LoadBundledRuleConfigs failed", err)
	if diags := vocabulary.ValidateBundled(reg, manifests, ruleConfigs); len(diags) > 0 {
		t.Fatalf("vocabulary.ValidateBundled failed: %v", diags)
	}
	options, err := manifests.Get("options", "1.0.0")
	testutil.FailErr(t, "manifests.Get options failed", err)
	if options.Topology != "decision-research-fanout" {
		t.Fatalf("topology = %q want decision-research-fanout", options.Topology)
	}
	if options.Trigger != "/options" {
		t.Fatalf("trigger = %q want /options", options.Trigger)
	}
	if options.Name != "Decide" {
		t.Fatalf("name = %q want Decide", options.Name)
	}
	if options.Request == nil || options.Request.Default != "" {
		t.Fatalf("options request = %+v", options.Request)
	}
	judge, ok := options.PhaseByID("judge")
	if !ok || judge.ReviewLoop == nil || judge.ReviewLoop.EvidenceKey != "options_judge" {
		t.Fatalf("judge review_loop = %+v", judge.ReviewLoop)
	}
	selectPhase, ok := options.PhaseByID("select")
	if !ok || selectPhase.HumanApproval == nil {
		t.Fatal("select human_approval missing")
	}
}

func TestManifestLoadValidationRejectsUnknownCompleteWhen(t *testing.T) {
	reg := conditions.NewRegistry()
	_ = reg.Register("plan_stub_valid", func(_ conditions.EvalContext) (bool, error) {
		return true, nil
	})
	manifests := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"bad@1.0.0": workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "bad",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{
				{ID: "only", CompleteWhen: "not_registered_ever"},
			},
		}),
	})
	diags := vocabulary.ValidateBundled(reg, manifests, nil)
	if len(diags) == 0 {
		t.Fatal("expected boot-time validation error for unknown complete_when")
	}
}
