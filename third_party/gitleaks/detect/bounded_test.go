package detect

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect/budget"
	"github.com/zricethezav/gitleaks/v8/regexp"
	"reflect"
	"strings"
	"testing"
)

func boundedFixture() *Detector {
	rule := config.Rule{RuleID: "fixture", Regex: regexp.MustCompile(`(?i)fixture_[a-z0-9]{12}`), Keywords: []string{"fixture"}}
	d := NewDetector(config.Config{Rules: map[string]config.Rule{"fixture": rule}, Keywords: map[string]struct{}{"fixture": {}, "fix": {}}})
	d.MaxDecodeDepth = 2
	return d
}
func boundedLimits() budget.Limits {
	return budget.Limits{MaxInputBytes: 1 << 20, MaxAllocationBytes: 256 << 20, MaxWork: 1 << 30, MaxMatches: 4096}
}
func TestBoundedPolicy(t *testing.T) {
	value := "fixture_123456abcdef"
	unicode := ""
	percent := ""
	for _, b := range []byte(value) {
		const digits = "0123456789abcdef"
		unicode += `\u00` + string(digits[b>>4]) + string(digits[b&15])
		percent += "%" + string(digits[b>>4]) + string(digits[b&15])
	}
	for _, raw := range []string{"inert", value, strings.ToUpper(value), hex.EncodeToString([]byte(value)), base64.StdEncoding.EncodeToString([]byte(value)), unicode, percent, base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString([]byte(value)))), value + " # gitleaks:allow"} {
		t.Run(raw, func(t *testing.T) {
			d := boundedFixture()
			fragment := Fragment{Raw: raw}
			want := d.DetectContext(context.Background(), fragment)
			got, err := d.DetectBounded(context.Background(), fragment, boundedLimits())
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("bounded differs: got %#v err %v; want %#v", got, err, want)
			}
		})
	}
}
func TestBoundedRefusals(t *testing.T) {
	for _, resource := range []string{"input_bytes", "allocation_bytes", "work", "match_candidates"} {
		t.Run(resource, func(t *testing.T) {
			limits := boundedLimits()
			raw := strings.Repeat("fixture_123456abcdef\n", 20)
			switch resource {
			case "input_bytes":
				limits.MaxInputBytes = 1
			case "allocation_bytes":
				limits.MaxAllocationBytes = 1
			case "work":
				limits.MaxWork = 1
			case "match_candidates":
				limits.MaxMatches = 2
			}
			got, err := boundedFixture().DetectBounded(context.Background(), Fragment{Raw: raw}, limits)
			var limit *budget.Error
			if !errors.As(err, &limit) || limit.Resource != resource || len(got) != 0 {
				t.Fatalf("partial or incorrect refusal: %d %v", len(got), err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := boundedFixture().DetectBounded(ctx, Fragment{Raw: "fixture_123456abcdef"}, boundedLimits())
	if !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("cancel: %d %v", len(got), err)
	}
}

func TestBoundedLocationGolden(t *testing.T) {
	for _, c := range []struct {
		raw        string
		start, end int
		want       Location
	}{
		{"abc", 0, 3, Location{startLine: 0, endLine: 0, startColumn: 1, endColumn: 3, startLineIndex: 0, endLineIndex: 3}},
		{"a\nbc\nz", 2, 4, Location{startLine: 1, endLine: 1, startColumn: 2, endColumn: 3, startLineIndex: 1, endLineIndex: 4}},
		{"a\nbc\nz", 5, 6, Location{startLine: 2, endLine: 2, startColumn: 2, endColumn: 2, startLineIndex: 0, endLineIndex: 6}},
	} {
		if got := locationRaw(c.raw, []int{c.start, c.end}, nil); got != c.want {
			t.Fatalf("raw location=%+v want %+v", got, c.want)
		}
	}
}

func TestBoundedPolicyGuardsAndRequiredRules(t *testing.T) {
	for _, mode := range []string{"keyword-case", "rule-allowlist", "global-allowlist", "path", "required"} {
		t.Run(mode, func(t *testing.T) {
			d := boundedFixture()
			rule := d.Config.Rules["fixture"]
			raw := "fixture_123456abcdef auxiliary_123"
			fragment := Fragment{Raw: raw, FilePath: "fixture.txt"}
			allow := &config.Allowlist{Regexes: []*regexp.Regexp{regexp.MustCompile("fixture_")}}
			if err := allow.Validate(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "keyword-case":
				d.Config.Keywords = map[string]struct{}{"FIXTURE": {}}
				d = NewDetector(d.Config)
			case "rule-allowlist":
				rule.Allowlists = []*config.Allowlist{allow}
				d.Config.Rules["fixture"] = rule
			case "global-allowlist":
				d.Config.Allowlists = []*config.Allowlist{allow}
			case "path":
				rule.Path = regexp.MustCompile(`other\.txt$`)
				d.Config.Rules["fixture"] = rule
			case "required":
				rule.RequiredRules = []*config.Required{{RuleID: "auxiliary"}}
				d.Config.Rules["fixture"] = rule
				d.Config.Rules["auxiliary"] = config.Rule{RuleID: "auxiliary", Regex: regexp.MustCompile(`auxiliary_[0-9]+`), SkipReport: true}
			}
			got, err := d.DetectBounded(context.Background(), fragment, boundedLimits())
			want := d.DetectContext(context.Background(), fragment)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("policy changed: %v got=%+v want=%+v", err, got, want)
			}
		})
	}
}

func TestBoundedCancellationCannotCompleteThroughAllowlist(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := boundedFixture()
	d.Config.Path = "fixture.txt"
	limits := boundedLimits()
	limits.Reserve = func(int64) error { cancel(); return nil }
	findings, err := d.DetectBounded(ctx, Fragment{Raw: "fixture_123456abcdef", FilePath: "fixture.txt"}, limits)
	if !errors.Is(err, context.Canceled) || len(findings) != 0 {
		t.Fatalf("canceled allowlist short circuit completed: %v %+v", err, findings)
	}
}

func TestBoundedUnrelatedPanicPropagates(t *testing.T) {
	defer func() {
		if got := recover(); got != "fixture panic" {
			t.Fatalf("unrelated panic swallowed or replaced: %v", got)
		}
	}()
	limits := boundedLimits()
	limits.Reserve = func(int64) error { panic("fixture panic") }
	_, _ = boundedFixture().DetectBounded(context.Background(), Fragment{Raw: "plain"}, limits)
}
