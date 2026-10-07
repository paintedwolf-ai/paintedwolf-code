package maintainability

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func TestMaintainabilityPolicyCoversEveryCategory(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testutil.CheckoutRoot(t), policyPath))
	testutil.FailErr(t, "read maintainability budgets", err)
	_, err = decodePolicy(raw)
	testutil.FailErr(t, "decode maintainability budgets", err)
}

func TestMaintainabilityPolicyRoundTrip(t *testing.T) {
	policy := sizebudget.Policy{
		Limits:        map[string]sizebudget.Limit{},
		Grandfathered: map[string]map[string]int{"source_files": {"b.go": 900, "a.go": 700}},
		Exceptions:    map[string]map[string]sizebudget.Exception{"test_files": {"t_test.go": {Cap: 1200, Reason: "one table per dialect"}}},
	}
	for _, name := range suite.CategoryNames() {
		policy.Limits[name] = sizebudget.Limit{Warn: 400, Limit: 600}
	}
	raw, err := encodePolicy(policy)
	testutil.FailErr(t, "encode policy", err)
	got, err := decodePolicy(raw)
	testutil.FailErr(t, "decode policy", err)
	if !reflect.DeepEqual(got, policy) {
		t.Fatalf("decoded policy = %#v, want %#v", got, policy)
	}
	again, err := encodePolicy(got)
	testutil.FailErr(t, "encode policy again", err)
	if string(raw) != string(again) {
		t.Fatal("policy encoding is not deterministic")
	}
	base := string(raw)
	for name, body := range map[string]string{
		"unknown field":      base + "notes: {}\n",
		"duplicate artifact": strings.ReplaceAll(base, "a.go: 700", "a.go: 700\n        a.go: 701"),
		"fractional cap":     strings.ReplaceAll(base, "a.go: 700", "a.go: 700.5"),
		"quoted cap":         strings.ReplaceAll(base, "a.go: 700", "a.go: '700'"),
		"cap within limit":   strings.ReplaceAll(base, "a.go: 700", "a.go: 600"),
		"missing reason":     strings.ReplaceAll(base, "reason: one table per dialect", "reason: ''"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePolicy([]byte(body)); err == nil {
				t.Fatalf("accepted invalid policy:\n%s", body)
			}
		})
	}
}

func TestMaintainabilityArtifactSources(t *testing.T) {
	inv := &inventory{measured: newMeasurements(), sources: map[string][]string{"pkg/p.Server": {"pkg/a.go", "pkg/b.go"}}}
	inv.measured["source_files"]["pkg/a.go"] = 10
	inv.measured["source_directories"]["pkg"] = 2
	inv.measured["go_receiver_lines"]["pkg/p.Server"] = 30
	got := artifactSources(inv)
	if !reflect.DeepEqual(got["source_files"]["pkg/a.go"], []string{"pkg/a.go"}) ||
		!reflect.DeepEqual(got["source_directories"]["pkg"], []string{"pkg"}) ||
		!reflect.DeepEqual(got["go_receiver_lines"]["pkg/p.Server"], []string{"pkg/a.go", "pkg/b.go"}) {
		t.Fatalf("artifact sources = %#v", got)
	}
}
