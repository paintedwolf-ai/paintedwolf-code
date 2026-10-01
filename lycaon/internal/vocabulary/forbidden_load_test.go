package vocabulary

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
)

func TestValidateBundledRejectsForbiddenRuleWhenLeaf(t *testing.T) {
	// Stage only the rule and diagnostic under test.
	diagnosticRel := config.SharedWorkflowDiag.Join("forbidden_predicate.yaml")
	diagnostic, err := config.Read(diagnosticRel)
	testutil.FailErr(t, "read forbidden predicate diagnostic", err)
	configtest.Only(t, map[config.Rel]string{
		config.PostureRulesDir.Join("bad.yaml"): `rules:
  - id: bad-rule
    when:
      test_evidence_passed: true
    then:
      deny:
        code: X
        message: y
`,
		diagnosticRel: string(diagnostic),
	})
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	configs, err := rules.LoadBundledRuleConfigs()
	testutil.FailErr(t, "load rule configs", err)
	diags := ValidateBundled(reg, nil, configs)
	if len(diags) == 0 {
		t.Fatal("expected forbidden rules error")
	}
	if sum := workflowdiag.Summarize(diags); !strings.Contains(sum, "evidence_passed:test") {
		t.Fatalf("error should cite replacement: %s", sum)
	}
}

func TestValidateBundledAcceptsValidManifest(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	manifests := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ok@1.0.0": workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "ok",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{
				ID:                "research",
				CompleteWhen:      "topology_stage_complete",
				BindTopologyStage: "research",
			}},
		}),
	})
	diags := ValidateBundled(reg, manifests, nil)
	if len(diags) != 0 {
		t.Fatalf("diags = %v", diags)
	}
}

func TestValidateBundledRejectsForbiddenCompleteWhen(t *testing.T) {
	reg := conditions.NewRegistry()
	if err := reg.Register("plan_stub_valid", func(_ conditions.EvalContext) (bool, error) { return true, nil }); err != nil {
		testutil.FailErr(t, "reg.Register failed", err)
	}
	manifests := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"bad@1.0.0": workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "bad",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{
				{ID: "x", CompleteWhen: "stage_plan_complete"},
			},
		}),
	})
	diags := ValidateBundled(reg, manifests, nil)
	if len(diags) == 0 {
		t.Fatal("expected forbidden complete_when error")
	}
	sum := workflowdiag.Summarize(diags)
	if !strings.Contains(sum, "bind_topology_stage") && !strings.Contains(diags[0].Replacement, "bind_topology_stage") {
		t.Fatalf("diags = %v", diags)
	}
}
