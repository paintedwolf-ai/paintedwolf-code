//go:build budgets

package maintainability

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

// TestMaintainabilityWithinBudget holds every artifact the change touches to
// its category limit or its exception, and every exception to its cap.
func TestMaintainabilityWithinBudget(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	change, err := sizebudget.LoadChangeSet()
	testutil.FailErr(t, "load change set", err)
	raw, err := os.ReadFile(filepath.Join(root, policyPath))
	testutil.FailErr(t, "read maintainability budgets", err)
	policy, err := decodePolicy(raw)
	testutil.FailErr(t, "decode maintainability budgets", err)
	inv, err := measureWorkingTree(t.Context(), root)
	testutil.FailErr(t, "measure maintained sources", err)

	isTouched := touched(inv, change)
	findings := sizebudget.Evaluate(policy, inv.measured, isTouched)
	basePolicy, ok, note, err := sizebudget.BasePolicy(root, change.Base, policyPath, decodePolicy)
	testutil.FailErr(t, "read base maintainability budgets", err)
	var notes []string
	if ok {
		findings = append(findings, sizebudget.ExceptionChanges(basePolicy, policy)...)
	} else {
		notes = append(notes, note)
	}
	report := sizebudget.NewReport(suite, policy, findings, sizebudget.Untouched(policy, inv.measured, isTouched), artifactSources(inv))
	report.Notes = append(report.Notes, notes...)
	testutil.FailErr(t, "write maintainability report", sizebudget.WriteReport(report))
	if failures := sizebudget.Failures(findings); len(failures) > 0 {
		t.Fatal(sizebudget.FailureReport(suite, failures, detail(inv)))
	}
}
