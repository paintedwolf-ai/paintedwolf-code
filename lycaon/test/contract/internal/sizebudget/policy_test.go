package sizebudget

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func testPolicy() Policy {
	return Policy{
		Limits:        map[string]Limit{"files": {Warn: 10, Limit: 20}},
		Grandfathered: map[string]map[string]int{"files": {"legacy": 30, "shrunk": 30, "fits": 25, "gone": 40}},
		Exceptions:    map[string]map[string]Exception{"files": {"special": {Cap: 50, Reason: "one table per wire type"}}},
	}
}

func TestEvaluateFailsOnlyOnGrowth(t *testing.T) {
	t.Parallel()
	measured := Measurements{"files": {
		"small": 5, "warned": 15, "new_big": 21,
		"legacy": 31, "shrunk": 28, "fits": 20,
		"special": 50,
	}}
	got := Evaluate(testPolicy(), measured)
	want := []Finding{
		{Category: "files", ID: "legacy", Kind: OverCap, Measured: 31, Bound: 30, Entry: EntryGrandfathered},
		{Category: "files", ID: "new_big", Kind: OverLimit, Measured: 21, Bound: 20},
		{Category: "files", ID: "warned", Kind: OverWarn, Measured: 15, Bound: 10},
		{Category: "files", ID: "shrunk", Kind: Slack, Measured: 28, Bound: 30, Entry: EntryGrandfathered},
		{Category: "files", ID: "fits", Kind: Unneeded, Measured: 20, Bound: 20, Entry: EntryGrandfathered},
		{Category: "files", ID: "gone", Kind: Vanished, Bound: 40, Entry: EntryGrandfathered},
	}
	sortFindings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %#v\nwant %#v", got, want)
	}
	failures := Failures(got)
	if len(failures) != 2 || failures[0].ID != "legacy" || failures[1].ID != "new_big" {
		t.Fatalf("failures = %#v, want legacy and new_big", failures)
	}
}

func TestEvaluateExceptionOverCapCarriesReason(t *testing.T) {
	t.Parallel()
	got := Failures(Evaluate(testPolicy(), Measurements{"files": {"special": 51}}))
	if len(got) != 1 || got[0].Kind != OverCap || got[0].Reason != "one table per wire type" {
		t.Fatalf("failures = %#v, want the exception's over_cap with its reason", got)
	}
}

func TestTightenOnlyLowersAndDrops(t *testing.T) {
	t.Parallel()
	measured := Measurements{"files": {"legacy": 35, "shrunk": 28, "fits": 20, "special": 30}}
	got := Tighten(testPolicy(), measured)
	want := map[string]map[string]int{"files": {"legacy": 30, "shrunk": 28}}
	if !reflect.DeepEqual(got.Grandfathered, want) {
		t.Fatalf("tightened grandfathered = %#v, want %#v", got.Grandfathered, want)
	}
	if !reflect.DeepEqual(got.Exceptions, testPolicy().Exceptions) {
		t.Fatalf("tighten changed hand-written exceptions: %#v", got.Exceptions)
	}
}

func TestGrandfatherGrowthFindsAddedAndRaisedCaps(t *testing.T) {
	t.Parallel()
	base := testPolicy()
	head := testPolicy()
	head.Grandfathered["files"]["legacy"] = 31
	head.Grandfathered["files"]["shrunk"] = 29
	head.Grandfathered["files"]["added"] = 22
	got := GrandfatherGrowth(base, head)
	sortFindings(got)
	want := []Finding{
		{Category: "files", ID: "added", Kind: GrandfatherRaised, Measured: 22, Entry: EntryGrandfathered},
		{Category: "files", ID: "legacy", Kind: GrandfatherRaised, Measured: 31, Bound: 30, Entry: EntryGrandfathered},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("growth = %#v\nwant %#v", got, want)
	}
}

func TestValidateRejectsPoliciesThatCannotBeReviewed(t *testing.T) {
	t.Parallel()
	categories := []string{"files"}
	if err := testPolicy().Validate(categories); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Policy){
		"missing limit":         func(p *Policy) { delete(p.Limits, "files") },
		"warn not below limit":  func(p *Policy) { p.Limits["files"] = Limit{Warn: 20, Limit: 20} },
		"unknown category":      func(p *Policy) { p.Limits["other"] = Limit{Warn: 1, Limit: 2} },
		"exception without why": func(p *Policy) { p.Exceptions["files"]["bare"] = Exception{Cap: 30} },
		"exception within limit": func(p *Policy) {
			p.Exceptions["files"]["small"] = Exception{Cap: 20, Reason: "r"}
		},
		"grandfathered within limit": func(p *Policy) { p.Grandfathered["files"]["small"] = 20 },
		"listed twice":               func(p *Policy) { p.Grandfathered["files"]["special"] = 60 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := testPolicy()
			mutate(&p)
			if err := p.Validate(categories); err == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
}

func TestFailureReportNamesTheRemedy(t *testing.T) {
	t.Parallel()
	suite := Suite{Name: "sample", PolicyPath: "policy.yaml", Categories: map[string]Category{"files": {Unit: "lines", Measures: "lines per file", Remedy: "split by responsibility"}}}
	failures := Failures(Evaluate(testPolicy(), Measurements{"files": {"legacy": 31, "new_big": 21, "special": 51}}))
	report := FailureReport(suite, failures, func(f Finding) []string { return []string{"source: " + f.ID + ".go"} })
	for _, want := range []string{
		"sample budget: 3 artifact(s)", "over_cap: files[\"legacy\"]", "measured: 31 lines | cap: 30 lines | over by 1",
		"may not grow", "over_limit: files[\"new_big\"]", "add an exception with a reason",
		"exception reason: one table per wire type", "source: special.go", "remedy: split by responsibility",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

func TestRequireIntegersRejectsTruncatableSizes(t *testing.T) {
	t.Parallel()
	for body, valid := range map[string]bool{
		"limits: {files: {warn: 1, limit: 2}}\nexceptions: {files: {a: {cap: 3, reason: 'x: 1.5'}}}\n": true,
		"limits: {files: {warn: 1, limit: 2.5}}\n":                                                     false,
		"grandfathered: {files: {a: '30'}}\n":                                                          false,
		"grandfathered: {files: {a: 3e2}}\n":                                                           false,
	} {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(body), &node); err != nil {
			t.Fatalf("parse %q: %v", body, err)
		}
		if err := RequireIntegers(node.Content[0]); (err == nil) != valid {
			t.Errorf("RequireIntegers(%q) error = %v, want valid=%v", body, err, valid)
		}
	}
}
