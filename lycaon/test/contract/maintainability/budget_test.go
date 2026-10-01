package maintainability

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMaintainabilityWithinBudget(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	sources, err := discoverSources(root)
	testutil.FailErr(t, "discover maintained sources", err)
	inv, err := measure(t.Context(), root, sources)
	testutil.FailErr(t, "measure maintained sources", err)
	budgets := filepath.Join(root, budgetPath)
	if os.Getenv("UPDATE_MAINTAINABILITY_BUDGETS") == "1" {
		raw, err := encodeBudgets(inv.measured)
		testutil.FailErr(t, "encode maintainability budgets", err)
		testutil.FailErr(t, "write maintainability budgets", os.WriteFile(budgets, raw, 0o644))
		return
	}
	raw, err := os.ReadFile(budgets)
	testutil.FailErr(t, "read maintainability budgets", err)
	caps, err := decodeBudgets(raw)
	testutil.FailErr(t, "decode maintainability budgets", err)
	if found := compareBudgets(inv.measured, caps); len(found) > 0 {
		t.Fatal(budgetReport(found, inv.sources))
	}
}

func TestMaintainabilityBudgetComparison(t *testing.T) {
	measured, caps := newMeasurements(), newMeasurements()
	measured["go_receiver_lines"] = map[string]int{"equal": 10, "smaller": 9, "larger": 11, "new": 0}
	caps["go_receiver_lines"] = map[string]int{"equal": 10, "smaller": 10, "larger": 10, "gone": 10}
	want := []violation{
		{"go_receiver_lines", "new", "missing_cap", 0, 0},
		{"go_receiver_lines", "larger", "over_cap", 11, 10},
		{"go_receiver_lines", "gone", "stale_cap", 0, 10},
	}
	got := compareBudgets(measured, caps)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("budget violations = %#v, want %#v", got, want)
	}
	report := budgetReport(got, map[string][]string{"larger": {"feature/one.go", "feature/two.go"}})
	for _, required := range []string{"over_cap", "missing_cap", "stale_cap", "excess: 1 lines", "source: feature/one.go", "source: feature/two.go", refreshCommand} {
		if !strings.Contains(report, required) {
			t.Errorf("failure report missing %q: %s", required, report)
		}
	}
}

func TestMaintainabilityBudgetRefreshRoundTrip(t *testing.T) {
	measured := newMeasurements()
	measured["source_files"]["b.go"] = 2
	measured["source_files"]["a.go"] = 0
	raw, err := encodeBudgets(measured)
	testutil.FailErr(t, "encode measured caps", err)
	got, err := decodeBudgets(raw)
	testutil.FailErr(t, "decode measured caps", err)
	if !reflect.DeepEqual(got, measured) {
		t.Fatalf("decoded caps = %#v, want %#v", got, measured)
	}
	again, err := encodeBudgets(got)
	testutil.FailErr(t, "encode caps again", err)
	if string(raw) != string(again) {
		t.Fatal("refresh output is not deterministic")
	}
	base := string(raw)
	for name, body := range map[string]string{
		"unknown category":   base + "unknown: {}\n",
		"duplicate category": base + "source_files: {}\n",
		"duplicate artifact": strings.ReplaceAll(base, "a.go: 0", "a.go: 0\n    a.go: 1"),
		"fractional cap":     strings.ReplaceAll(base, "a.go: 0", "a.go: 1.5"),
		"negative cap":       strings.ReplaceAll(base, "a.go: 0", "a.go: -1"),
		"quoted cap":         strings.ReplaceAll(base, "a.go: 0", "a.go: '1'"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeBudgets([]byte(body)); err == nil {
				t.Fatalf("accepted invalid budget:\n%s", body)
			}
		})
	}
}
