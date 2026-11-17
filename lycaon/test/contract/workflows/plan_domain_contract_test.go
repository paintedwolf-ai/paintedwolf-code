package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPlanDomainIDsRegisteredOnBoot(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	for _, id := range conditions.ShippedPlanDomainIDs() {
		if !reg.Has(id) {
			t.Fatalf("shipped plan domain id %q not registered", id)
		}
	}
}

func TestBundledWorkflowsOnlyReferenceImplementedPlanPredicates(t *testing.T) {
	manifests, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		contractcheck.FailErr(t, "register rule conditions", err)
	}
	ruleConfigs, err := rules.LoadBundledRuleConfigs()
	contractcheck.FailErr(t, "rules.LoadBundledRuleConfigs failed", err)
	if diags := vocabulary.ValidateBundled(reg, manifests, ruleConfigs); len(diags) > 0 {
		t.Fatalf("vocabulary.ValidateBundled failed: %v", diags)
	}
}
