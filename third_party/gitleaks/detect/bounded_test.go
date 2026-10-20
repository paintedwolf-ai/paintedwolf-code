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
