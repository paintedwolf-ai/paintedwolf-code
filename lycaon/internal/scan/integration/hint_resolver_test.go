package integration

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/scan/hints"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanHintResolverMapsRuleIDs(t *testing.T) {
	root := configlayout.FindModuleRoot()
	cfg, err := hints.Load(filepath.Join(root, "config", "runtime", "scanners", "scan-hints.yaml"))
	testutil.FailErr(t, "hints.Load failed", err)
	resolver := scan.NewScanHintResolver(cfg)
	out := resolver.Resolve([]api.SecurityFinding{
		scanfindings.FixtureFinding("lycaon.ruby.sql-string-concat", api.FindingLevelHigh, "sql concat", "", 0),
	})
	if len(out) != 1 || out[0].Code != "SCAN_SQL_INJECTION" {
		t.Fatalf("guidance = %#v", out)
	}
}

// Only ":"-terminated keys are namespaces; the longest match wins.
func TestScanHintResolverNamespacePrefixPrecedence(t *testing.T) {
	cfg := &hints.Config{
		Hints: map[string]hints.HintEntry{
			"SCAN_BROAD":  {Message: "broad", Severity: "warning"},
			"SCAN_NARROW": {Message: "narrow", Severity: "error"},
			"SCAN_EXACT":  {Message: "exact", Severity: "error"},
			"SCAN_FALLBACK": {
				Message:  "fallback",
				Severity: "warning",
			},
		},
		RuleHints: map[string]string{
			"semgrep:":          "SCAN_BROAD",
			"semgrep:security:": "SCAN_NARROW",
			"vendor.rule-exact": "SCAN_EXACT",
		},
		DefaultHint: "SCAN_FALLBACK",
	}
	resolver := scan.NewScanHintResolver(cfg)

	for _, tc := range []struct {
		name   string
		ruleID string
		want   string
	}{
		{"longest namespace wins", "semgrep:security:sqli", "SCAN_NARROW"},
		{"shorter namespace still matches", "semgrep:style:naming", "SCAN_BROAD"},
		{"exact key is not a prefix", "vendor.rule-exact-v2", "SCAN_FALLBACK"},
		{"exact key still matches exactly", "vendor.rule-exact", "SCAN_EXACT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Repetition exposes order-dependent resolution.
			for i := 0; i < 32; i++ {
				out := resolver.Resolve([]api.SecurityFinding{
					scanfindings.FixtureFinding(tc.ruleID, api.FindingLevelHigh, "finding", "", 0),
				})
				if len(out) != 1 || out[0].Code != tc.want {
					t.Fatalf("guidance = %#v, want code %s", out, tc.want)
				}
			}
		})
	}
}

func TestScanHintResolverScenariosFromConfig(t *testing.T) {
	root := configlayout.FindModuleRoot()
	cfg, err := hints.Load(filepath.Join(root, "config", "runtime", "scanners", "scan-hints.yaml"))
	testutil.FailErr(t, "hints.Load failed", err)
	resolver := scan.NewScanHintResolver(cfg)
	for _, sc := range cfg.Scenarios {
		ruleID, _ := sc.Finding["rule_id"].(string)
		sev, _ := sc.Finding["severity"].(string)
		want, _ := sc.Expect["hint_code"].(string)
		got := resolver.Resolve([]api.SecurityFinding{
			scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
				DriverID: "fixture",
				RuleID:   ruleID,
				Level:    scanfindings.NormalizeVendorSeverity(sev),
				Kind:     kindForScenario(ruleID),
			}),
		})
		if len(got) != 1 || got[0].Code != want {
			t.Fatalf("scenario %s: got %#v want %s", sc.ID, got, want)
		}
	}
}

func TestScanHintResolverFallbackUnmapped(t *testing.T) {
	root := configlayout.FindModuleRoot()
	cfg, err := hints.Load(filepath.Join(root, "config", "runtime", "scanners", "scan-hints.yaml"))
	testutil.FailErr(t, "hints.Load failed", err)
	resolver := scan.NewScanHintResolver(cfg)
	out := resolver.Resolve([]api.SecurityFinding{
		scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID: "fixture",
			RuleID:   "vendor.unknown.rule",
			Level:    api.FindingLevelInfo,
			Kind:     api.FindingKindCustom,
		}),
	})
	if len(out) != 1 || out[0].Code != "SCAN_FINDING_UNMAPPED" {
		t.Fatalf("guidance = %#v", out)
	}
}

func kindForScenario(ruleID string) api.FindingKind {
	if ruleID == "vendor.unknown.rule" {
		return api.FindingKindCustom
	}
	return api.FindingKindSAST
}
