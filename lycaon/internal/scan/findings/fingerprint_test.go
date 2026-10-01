package findings_test

import (
	"testing"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPrimaryFingerprintStable(t *testing.T) {
	f := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "opengrep",
		RuleID:   "opengrep:test",
		Level:    api.FindingLevelHigh,
		Message:  "msg",
		Kind:     api.FindingKindSAST,
		Locations: []api.SecurityFindingLocation{{
			URI:       "src/a.go",
			StartLine: 10,
		}},
	})
	want := scanfindings.PrimaryFingerprint("opengrep", api.FindingKindSAST, "opengrep:test", "src/a.go", 10, "")
	if f.Fingerprints.Primary != want {
		t.Fatalf("primary = %q want %q", f.Fingerprints.Primary, want)
	}
}

func TestSeverityEnrichmentNeverChangesPrimaryFingerprint(t *testing.T) {
	build := func(ruleID string, level api.FindingLevel) api.SecurityFinding {
		return scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID: "semgrep-custom", RuleID: ruleID, Level: level, Kind: api.FindingKindSAST,
			Locations: []api.SecurityFindingLocation{{URI: "pkg/auth.go", StartLine: 42}},
		})
	}
	// CVE-2026-33810 is in the bundled catalog; CVE-2099-12345 is not.
	for _, ruleID := range []string{"rules.security.CVE-2026-33810.exploit", "rules.security.CVE-2099-12345.exploit"} {
		unrated, rated := build(ruleID, api.FindingLevelUnknown), build(ruleID, api.FindingLevelMedium)
		if unrated.Fingerprints.Primary != rated.Fingerprints.Primary {
			t.Fatalf("%s: enrichment changed identity: %s vs %s", ruleID, unrated.Fingerprints.Primary, rated.Fingerprints.Primary)
		}
		refreshed := unrated
		scanfindings.RefreshFingerprint(&refreshed)
		if refreshed.Fingerprints.Primary != unrated.Fingerprints.Primary {
			t.Fatalf("%s: refreshed identity differs after enrichment", ruleID)
		}
	}
	if resolved := build("rules.security.CVE-2026-33810.exploit", api.FindingLevelUnknown); resolved.Level != api.FindingLevelCritical {
		t.Fatalf("catalog did not rate the finding: %s", resolved.Level)
	}
}
