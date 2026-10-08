package sizebudget

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func testPolicy() Policy {
	return Policy{
		Limits: map[string]Limit{"files": {Warn: 10, Limit: 20}},
		Exceptions: map[string]map[string]Exception{"files": {
			"special":  {Cap: 50, Reason: "one table per wire type"},
			"outgrown": {Cap: 30, Reason: "r"}, "fits": {Cap: 30, Reason: "r"}, "gone": {Cap: 40, Reason: "r"},
		}},
	}
}

func touchedOnly(ids ...string) Touched {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(_, id string) bool { return set[id] }
}

func kinds(findings []Finding) map[string]Kind {
	out := map[string]Kind{}
	for _, f := range findings {
		out[f.ID] = f.Kind
	}
	return out
}

func TestEvaluateHoldsTouchedArtifactsToAnAbsoluteStandard(t *testing.T) {
	t.Parallel()
	measured := Measurements{"files": {
		"big": 21, "huge_untouched": 500, "warm": 15, "warm_untouched": 15, "small": 5,
		"special": 45, "outgrown": 31, "fits": 20,
	}}
	touched := touchedOnly("big", "warm", "small", "special")
	got := kinds(Evaluate(testPolicy(), measured, touched))
	want := map[string]Kind{
		"big": OverLimit, "warm": OverWarn, "special": Excepted,
		"outgrown": OverCap, "fits": Unneeded, "gone": Vanished,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("finding kinds = %v\nwant %v", got, want)
	}
	failing := map[string]bool{}
	for _, f := range Failures(Evaluate(testPolicy(), measured, touched)) {
		failing[f.ID] = true
	}
	if !reflect.DeepEqual(failing, map[string]bool{"big": true, "outgrown": true}) {
		t.Fatalf("failures = %v, want the touched oversized artifact and the outgrown exception", failing)
	}
	if got := Untouched(testPolicy(), measured, touched); !reflect.DeepEqual(got, map[string]int{"files": 1}) {
		t.Fatalf("untouched past limit = %v, want 1", got)
	}
}

func TestEvaluateCarriesTheExceptionReason(t *testing.T) {
	t.Parallel()
	for _, f := range Evaluate(testPolicy(), Measurements{"files": {"special": 45, "outgrown": 31}}, touchedOnly("special")) {
		if f.Reason == "" || (f.ID == "special" && f.Bound != 50) {
			t.Errorf("finding %+v lacks its exception's reason or cap", f)
		}
	}
}

func TestExceptionChangesNameAddedAndRaisedCaps(t *testing.T) {
	t.Parallel()
	head := testPolicy()
	head.Exceptions["files"]["special"] = Exception{Cap: 60, Reason: "grew a wire type"}
	head.Exceptions["files"]["added"] = Exception{Cap: 22, Reason: "new"}
	head.Exceptions["files"]["fits"] = Exception{Cap: 25, Reason: "lowered"}
	got := ExceptionChanges(testPolicy(), head)
	if len(got) != 2 || got[0].ID != "added" || got[0].Previous != nil || got[1].ID != "special" || *got[1].Previous != 50 || got[1].Measured != 60 {
		t.Fatalf("exception changes = %+v", got)
	}
}

func TestValidateRejectsPoliciesThatCannotBeReviewed(t *testing.T) {
	t.Parallel()
	categories := []string{"files"}
	if err := testPolicy().Validate(categories); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Policy){
		"missing limit":          func(p *Policy) { delete(p.Limits, "files") },
		"warn not below limit":   func(p *Policy) { p.Limits["files"] = Limit{Warn: 20, Limit: 20} },
		"unknown category":       func(p *Policy) { p.Limits["other"] = Limit{Warn: 1, Limit: 2} },
		"exception without why":  func(p *Policy) { p.Exceptions["files"]["bare"] = Exception{Cap: 30} },
		"exception within limit": func(p *Policy) { p.Exceptions["files"]["small"] = Exception{Cap: 20, Reason: "r"} },
		"exception for unknown":  func(p *Policy) { p.Exceptions["other"] = map[string]Exception{"x": {Cap: 99, Reason: "r"}} },
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

func TestRequireIntegersRejectsTruncatableSizes(t *testing.T) {
	t.Parallel()
	for body, valid := range map[string]bool{
		"limits: {files: {warn: 1, limit: 2}}\nexceptions: {files: {a: {cap: 3, reason: 'x: 1.5'}}}\n": true,
		"limits: {files: {warn: 1, limit: 2.5}}\n":                                                     false,
		"exceptions: {files: {a: {cap: '30', reason: r}}}\n":                                           false,
		"exceptions: {files: {a: {cap: 3e2, reason: r}}}\n":                                            false,
		"limits: {files: [1, 2]}\n":                                                                    false,
		"base: &b {warn: 1, limit: 2}\nlimits: {files: *b}\n":                                          false,
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

func TestFailureReportNamesTheRemedy(t *testing.T) {
	t.Parallel()
	suite := Suite{Name: "sample", PolicyPath: "policy.yaml", Categories: map[string]Category{"files": {Unit: "lines", Measures: "lines per file", Remedy: "split by responsibility"}}}
	failures := Failures(Evaluate(testPolicy(), Measurements{"files": {"big": 21, "outgrown": 31}}, touchedOnly("big")))
	report := FailureReport(suite, failures, func(f Finding) []string { return []string{"source: " + f.ID + ".go"} })
	for _, want := range []string{
		"sample budget: 2 artifact(s)", `over_limit: files["big"]`, "measured: 21 lines | limit: 20 lines | over by 1",
		"bring it within its limit", `over_cap: files["outgrown"]`, "exception cap: 30", "revisit the reason",
		"source: big.go", "remedy: split by responsibility",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

func TestChangeSetNamesWhatAChangeTouched(t *testing.T) {
	t.Parallel()
	change := &ChangeSet{
		Lines:   map[string][]int{"pkg/a.go": {10, 11}, "web/new.ts": {1, 2}},
		Added:   []string{"web/new.ts"},
		Removed: []string{"old/gone.go"},
		Inspect: []string{"docs/big.md", "deep"},
	}
	for name, got := range map[string]bool{
		"edited file":              change.TouchesFile("pkg/a.go"),
		"inspected file":           change.TouchesFile("docs/big.md"),
		"edited lines in range":    change.TouchesLines("pkg/a.go", 5, 10),
		"directory gained a file":  change.TouchesDirectory("web"),
		"directory lost a file":    change.TouchesDirectory("old"),
		"inspected directory tree": change.TouchesDirectory("deep/er"),
		"source directory edited":  change.TouchesAny([]string{"pkg"}),
	} {
		if !got {
			t.Errorf("%s: not touched", name)
		}
	}
	for name, got := range map[string]bool{
		"unedited file":                change.TouchesFile("pkg/b.go"),
		"edited file, other lines":     change.TouchesLines("pkg/a.go", 12, 40),
		"directory with edits only":    change.TouchesDirectory("pkg"),
		"sibling with a shared prefix": change.TouchesAny([]string{"pk"}),
	} {
		if got {
			t.Errorf("%s: touched", name)
		}
	}
}
