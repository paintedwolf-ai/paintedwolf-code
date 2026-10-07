//go:build budgets

package maintainability

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func TestMaintainabilityWithinBudget(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	sources, err := discoverSources(root)
	testutil.FailErr(t, "discover maintained sources", err)
	inv, err := measure(t.Context(), root, sources)
	testutil.FailErr(t, "measure maintained sources", err)
	file := filepath.Join(root, policyPath)
	raw, err := os.ReadFile(file)
	testutil.FailErr(t, "read maintainability budgets", err)
	policy, err := decodePolicy(raw)
	testutil.FailErr(t, "decode maintainability budgets", err)
	if os.Getenv("UPDATE_MAINTAINABILITY_BUDGETS") == "1" {
		policy = sizebudget.Tighten(policy, inv.measured)
		raw, err := encodePolicy(policy)
		testutil.FailErr(t, "encode tightened budgets", err)
		testutil.FailErr(t, "write tightened budgets", os.WriteFile(file, raw, 0o644))
	}
	findings := sizebudget.Evaluate(policy, inv.measured)
	var notes []string
	base, ok, note, err := sizebudget.BasePolicy(root, policyPath, decodePolicy)
	testutil.FailErr(t, "read base maintainability budgets", err)
	if ok {
		findings = append(findings, sizebudget.GrandfatherGrowth(base, policy)...)
	} else {
		notes = append(notes, note)
	}
	report := sizebudget.NewReport(suite, policy, inv.measured, artifactSources(inv), findings, notes)
	testutil.FailErr(t, "write maintainability report", sizebudget.WriteReport(report))
	if failures := sizebudget.Failures(findings); len(failures) > 0 {
		t.Fatal(sizebudget.FailureReport(suite, failures, detail(inv)))
	}
}
