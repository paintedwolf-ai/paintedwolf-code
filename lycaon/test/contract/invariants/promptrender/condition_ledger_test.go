package promptrender_test

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

// Every atom of every compound stock guard either flips the guard's actual
// decision between two observed evaluations that hold the other atoms fixed,
// or is ruled out by the guard's logic or a recorded production constraint.
// The ledger is logged as the receipt.
func TestGuardAtomsHaveIndependentWitnesses(t *testing.T) {
	stock, err := promptrender.LoadStock(t.Context())
	if err != nil {
		t.Fatalf("load stock catalog: %v", err)
	}
	ledger, err := promptrender.StockConditionLedger(t.Context(), stock)
	if err != nil {
		t.Fatalf("build condition ledger: %v", err)
	}
	raw, err := json.Marshal(ledger)
	if err != nil {
		t.Fatalf("encode condition ledger: %v", err)
	}
	t.Logf("condition ledger: sites=%d compound=%d obligations=%d witnesses=%d exclusions=%d gaps=%d stale=%d",
		len(ledger.Sites), ledger.CompoundSites, ledger.Obligations, len(ledger.Witnesses), len(ledger.Exclusions), len(ledger.Gaps), len(ledger.StaleConstraints))
	t.Logf("condition ledger JSON:\n%s", raw)
	sum, err := promptrender.RunCartesianMatrix(t.Context(), stock)
	if err != nil {
		t.Fatalf("host-state matrix: %v", err)
	}
	cov := sum.Coverage
	t.Logf("render coverage: registered=%d rendered=%d unrendered=%d expected_unrendered=%d rules=%d template_branches=%d rule_branches=%d observed_families=%v observed_recipients=%v",
		len(cov.DiscoveredTemplates), cov.RenderedTemplates, len(cov.UnrenderedTemplates), len(cov.ExpectedUnrendered),
		len(cov.DiscoveredRules), len(cov.TemplateBranches), len(cov.RuleBranches), ledger.ObservedFamilies, ledger.ObservedRecipients)
	t.Logf("unrendered templates: %v", cov.UnrenderedTemplates)
	if err := ledger.RequireObserved([]string{"template", "oar"}, []string{interactions.RecipientCoordinator, interactions.RecipientWorker}); err != nil {
		t.Error(err)
	}
	if err := ledger.ValidateZeroUnexplainedGaps(); err != nil {
		t.Fatal(err)
	}
}
