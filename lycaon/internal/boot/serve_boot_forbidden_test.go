package boot

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
)

func TestServeBootFailsOnForbiddenBundledRule(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	ruleConfigs, err := rules.LoadBundledRuleConfigs()
	testutil.FailErr(t, "rules.LoadBundledRuleConfigs failed", err)
	cfgs := append([]*rules.RulesConfig(nil), ruleConfigs...)
	if diags := vocabulary.ValidateBundled(reg, manifests, cfgs); len(diags) > 0 {
		t.Fatalf("bundled config should validate: %v", diags)
	}

	// Simulate forbidden rule file at boot validation boundary.
	bad := &rules.RulesConfig{Rules: []rules.RuleEntry{{
		ID: "forbidden-rule",
		When: map[string]any{
			"test_evidence_passed": true,
		},
		Then: map[string]any{
			"deny": map[string]any{"code": "X", "message": "y"},
		},
	}}}
	diags := vocabulary.ValidateBundled(reg, manifests, []*rules.RulesConfig{bad})
	if len(diags) == 0 {
		t.Fatal("expected boot validation failure for forbidden when leaf")
	}
	sum := workflowdiag.Summarize(diags)
	if !strings.Contains(sum, "evidence_passed:test") && !strings.Contains(diags[0].Replacement, "evidence_passed:test") {
		t.Fatalf("diags = %v", diags)
	}
}
