package composition_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func testComposePolicy(t *testing.T) *workflowcomposition.ComposePolicy {
	t.Helper()
	policy, err := workflowcomposition.LoadComposePolicy()
	testutil.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	return policy
}

func testTemplates(t *testing.T) workflowcomposition.TemplateCatalog {
	t.Helper()
	dir := filepath.Join(bundledWorkflowsDir(t), "_templates")
	catalog, err := workflowcomposition.LoadTemplatesFromDir(extpacks.OnDisk(dir))
	testutil.FailErr(t, "workflowcomposition.LoadTemplatesFromDir failed", err)
	return catalog
}

func TestComposePolicyRequireExtends(t *testing.T) {
	p := testComposePolicy(t)
	errs := p.Apply(workflowcomposition.ComposePolicyInput{
		Raw:            workflowdef.Manifest{ID: "x", Version: "1.0.0"},
		Effective:      workflowdef.FinalizeManifest(workflowdef.Manifest{ID: "x", Version: "1.0.0", PhaseDefs: []workflowdef.PhaseDef{{ID: "only", CompleteWhen: "plan_stub_valid"}}}),
		SessionPosture: api.SessionPostureSpec,
	})
	if len(errs) == 0 || errs[0].Code != "extends_required" {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestComposePolicyAllowedExtendsReject(t *testing.T) {
	p := testComposePolicy(t)
	errs := p.Apply(workflowcomposition.ComposePolicyInput{
		ExtendsRef:     "missing@9.9.9",
		Effective:      workflowdef.FinalizeManifest(workflowdef.Manifest{ID: "x", Version: "1.0.0", Extends: "missing@9.9.9", PhaseDefs: []workflowdef.PhaseDef{{ID: "implement", CompleteWhen: "delegation_closeout_complete"}}}),
		SessionPosture: api.SessionPostureSpec,
	})
	found := false
	for _, e := range errs {
		if e.Code == "extends_not_allowed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestComposePolicyVetRequiresSecurityGate(t *testing.T) {
	p := testComposePolicy(t)
	effective := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "vet-wf", Version: "1.0.0", Extends: "plan@1.0.0", InitialPosture: "vet",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "stub", Next: "build"},
			{ID: "build", CompleteWhen: "plan_stub_valid"},
		},
	})
	errs := p.Apply(workflowcomposition.ComposePolicyInput{
		ExtendsRef:     "plan@1.0.0",
		Effective:      effective,
		SessionPosture: api.SessionPostureVet,
	})
	found := false
	for _, e := range errs {
		if e.Code == "required_gate_missing" && e.Message != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestComposePolicyBuildRequiresDelegationOnTerminal(t *testing.T) {
	p := testComposePolicy(t)
	effective := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "build-wf", Version: "1.0.0", Extends: "plan@1.0.0", InitialPosture: "build",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "stub", Next: "build"},
			{ID: "build", CompleteWhen: "plan_stub_valid"},
		},
	})
	errs := p.Apply(workflowcomposition.ComposePolicyInput{
		ExtendsRef:     "plan@1.0.0",
		Effective:      effective,
		SessionPosture: api.SessionPostureBuild,
	})
	found := false
	for _, e := range errs {
		if e.Code == "required_gate_missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestComposePolicyRequiredPhaseWhenExtends(t *testing.T) {
	p := testComposePolicy(t)
	effective := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "no-impl", Version: "1.0.0", Extends: "plan@1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{ID: "stub", CompleteWhen: "plan_stub_valid"}},
	})
	errs := p.Apply(workflowcomposition.ComposePolicyInput{
		ExtendsRef:     "plan@1.0.0",
		Effective:      effective,
		SessionPosture: api.SessionPostureSpec,
	})
	found := false
	for _, e := range errs {
		if e.Code == "required_phase_missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestPlanManifestKeepsAutoApproveParameter(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	plan, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "Get plan", err)
	if _, ok := plan.Parameters["auto_approve"]; !ok {
		t.Fatal("plan must keep auto_approve parameter")
	}
}

func TestWorkflowTemplateMissingRequiredParam(t *testing.T) {
	catalog := testTemplates(t)
	tmpl := catalog["hotfix-template"]
	if tmpl == nil {
		t.Fatal("hotfix-template missing")
	}
	_, err := tmpl.Expand(map[string]any{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestComposeFromTemplateClarifySummary(t *testing.T) {
	c := testComposer(t)
	c.Templates = testTemplates(t)
	result, err := c.ComposeFromTemplate(context.Background(), workflowcomposition.ComposeFromTemplateRequest{
		SessionID:  "sess-1",
		TemplateID: "clarify-then-implement-template",
		Params: map[string]any{
			"workflow_id": "clarify-wf",
			"question":    "Which API surface?",
		},
		CreatedBy: "coordinator",
	})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if len(result.EffectiveSummary.Feedback) != 1 {
		t.Fatalf("feedback = %+v", result.EffectiveSummary.Feedback)
	}
	if result.EffectiveSummary.Feedback[0].Prompt != "Which API surface?" {
		t.Fatalf("prompt = %q", result.EffectiveSummary.Feedback[0].Prompt)
	}
}

func TestComposeFromTemplateDecisionSummary(t *testing.T) {
	c := testComposer(t)
	c.Templates = testTemplates(t)
	result, err := c.ComposeFromTemplate(context.Background(), workflowcomposition.ComposeFromTemplateRequest{
		SessionID:  "sess-1",
		TemplateID: "confirm-skip-research-template",
		Params:     map[string]any{"workflow_id": "confirm-wf"},
		CreatedBy:  "coordinator",
	})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if len(result.EffectiveSummary.Decisions) != 1 {
		t.Fatalf("decisions = %+v", result.EffectiveSummary.Decisions)
	}
	if len(result.EffectiveSummary.Decisions[0].Options) < 2 {
		t.Fatalf("options = %+v", result.EffectiveSummary.Decisions[0].Options)
	}
}
