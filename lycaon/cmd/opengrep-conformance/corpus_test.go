package main

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestCompareRejectsMissingExecutionAndWrongFindings(t *testing.T) {
	cases := map[string]sourceCase{"source/example.js": {Name: "example", Want: []string{"rule"}}}
	if compare(&scanReport{}, cases) == nil {
		t.Fatal("unscanned positive appeared clean")
	}
	report := &scanReport{}
	report.Paths.Scanned = []string{"source/example.js"}
	if compare(report, cases) == nil {
		t.Fatal("missing finding accepted")
	}
	cases["source/example.js"] = sourceCase{Name: "safe"}
	testutil.FailErr(t, "safe scanned case", compare(report, cases))
	report.Paths.Scanned = nil
	if compare(report, cases) == nil {
		t.Fatal("unscanned safe case accepted")
	}
}

func TestMaterializeRejectsDuplicateAndEscapingCases(t *testing.T) {
	tc := sourceCase{Name: "same", File: "sample.js", Source: "const x=1;"}
	for _, items := range [][]sourceCase{{tc, tc}, {{Name: "bad", File: "../../outside.js", Source: "x"}}} {
		if _, err := materialize(t.TempDir(), []suite{{Language: "javascript", Cases: items}}, ""); err == nil {
			t.Fatal("invalid corpus accepted")
		}
	}
}
