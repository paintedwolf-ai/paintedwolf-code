//go:build budgets

package maintainability

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

// TestMaintainabilityWithinBudget rejects new excess and growth while reporting legacy debt.
func TestMaintainabilityWithinBudget(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	change, err := sizebudget.LoadChangeSet()
	testutil.FailErr(t, "load change set", err)
	raw, err := os.ReadFile(filepath.Join(root, policyPath))
	testutil.FailErr(t, "read maintainability budgets", err)
	policy, err := decodePolicy(raw)
	testutil.FailErr(t, "decode maintainability budgets", err)
	tree, err := openWorkingTree(root)
	testutil.FailErr(t, "open budget tree", err)
	testutil.FailErr(t, "load artifact exceptions", loadExceptions(tree, &policy))
	inv, err := measureWorkingTree(t.Context(), root)
	testutil.FailErr(t, "measure maintained sources", err)

	isTouched := touched(inv, change)
	baseTree, err := openBaseTree(root, change.Base)
	testutil.FailErr(t, "read budget base", err)
	baseSources, err := discoverSources(baseTree)
	testutil.FailErr(t, "discover budget base", err)
	baseInventory, err := measure(t.Context(), baseTree, baseSources)
	testutil.FailErr(t, "measure budget base", err)
	findings := sizebudget.EvaluateGrowth(policy, baseInventory.measured, inv.measured, isTouched)
	basePolicy, ok, note, err := sizebudget.BasePolicy(root, change.Base, policyPath, decodePolicy)
	testutil.FailErr(t, "read base maintainability budgets", err)
	var notes []string
	if ok {
		testutil.FailErr(t, "load base artifact exceptions", loadExceptions(baseTree, &basePolicy))
		findings = append(findings, sizebudget.ExceptionChanges(basePolicy, policy)...)
	} else {
		notes = append(notes, note)
	}
	report := sizebudget.NewReport(suite, policy, findings, sizebudget.Untouched(policy, inv.measured, isTouched), artifactSources(inv))
	report.Tracking = trackingReport(policy, inv, isTouched)
	report.Notes = append(report.Notes, notes...)
	testutil.FailErr(t, "write maintainability report", sizebudget.WriteReport(report))
	if failures := sizebudget.Failures(findings); len(failures) > 0 {
		t.Fatal(sizebudget.FailureReport(suite, failures, detail(inv)))
	}
}
