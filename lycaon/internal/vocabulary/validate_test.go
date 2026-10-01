package vocabulary

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBundledManifestLoadValidation(t *testing.T) {
	// The shipped workflow catalog is the embedded one; no disk layer and no
	// project overlay participate here.
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	ruleConfigs, err := rules.LoadBundledRuleConfigs()
	testutil.FailErr(t, "rules.LoadBundledRuleConfigs failed", err)
	if diags := ValidateBundled(reg, manifests, ruleConfigs); len(diags) > 0 {
		t.Fatalf("ValidateBundled failed: %v", diags)
	}
}

func TestValidateBundledRejectsDeferredScanCatalogStub(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	manifests := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"sec@1.0.0": workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "sec",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{
				{ID: "triage", CompleteWhen: "findings_triaged"},
			},
		}),
	})
	diags := ValidateBundled(reg, manifests, nil)
	if len(diags) == 0 {
		t.Fatal("expected validation error for deferred scan catalog stub")
	}
	if summary := fmt.Sprint(diags); !strings.Contains(summary, "findings_triaged") {
		t.Fatalf("diagnostics do not identify the deferred predicate: %v", diags)
	}
}

func TestValidateBundledRejectsUnknownCompleteWhen(t *testing.T) {
	reg := conditions.NewRegistry()
	manifests := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"bad@1.0.0": workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "bad",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{
				{ID: "x", CompleteWhen: "not_registered_ever"},
			},
		}),
	})
	diags := ValidateBundled(reg, manifests, nil)
	if len(diags) == 0 {
		t.Fatal("expected validation error")
	}
	if diags[0].Code != string(workflowdiag.MustCode("unknown_predicate")) {
		t.Fatalf("code = %q want %q", diags[0].Code, workflowdiag.MustCode("unknown_predicate"))
	}
}

func TestBundledSpecRulesResolveViaRegistry(t *testing.T) {
	cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join("spec.yaml"))
	testutil.FailErr(t, "load rules config YAML", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	if diags := ValidateBundled(reg, nil, []*rules.RulesConfig{cfg}); len(diags) > 0 {
		t.Fatalf("ValidateBundled failed: %v", diags)
	}
	engine := rules.NewSimpleEngine(cfg, reg)
	out, err := engine.Evaluate(t.Context(), rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureSpec, ToolName: "delegate_dispatch"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed {
		t.Fatal("expected deny for delegation in spec posture")
	}
	if out.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("code = %q", out.Code)
	}
}
