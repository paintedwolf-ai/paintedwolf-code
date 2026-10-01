package findings_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/internal/advisory/severity"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEnrichSecurityFindingResolvesAdvisoryCVE(t *testing.T) {
	adv := advisory.BuildAdvisoryRef("GO-2026-4599", "CVE-2026-27137")
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "lycaon-sca",
		RuleID:   advisory.RuleID(adv),
		Kind:     api.FindingKindSCA,
		Level:    api.FindingLevelUnknown,
		Advisory: adv,
		Locations: []api.SecurityFindingLocation{{
			URI: "go.mod",
		}},
	})

	if finding.Level != api.FindingLevelHigh {
		t.Fatalf("expected level High from nvd.cvss, got %s", finding.Level)
	}
	resolvedAdv := scanfindings.Advisory(finding)
	if resolvedAdv == nil || resolvedAdv.SeveritySource != severity.SourceNVDCVSS {
		t.Fatalf("expected severity_source %s, got %#v", severity.SourceNVDCVSS, resolvedAdv)
	}
	if len(resolvedAdv.CVSS) == 0 || resolvedAdv.CVSS[0].Score == nil || *resolvedAdv.CVSS[0].Score != 7.5 {
		t.Fatalf("expected CVSS score 7.5, got %#v", resolvedAdv.CVSS)
	}
}

func TestEnrichSecurityFindingResolvesEmbeddedRuleCVE(t *testing.T) {
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "semgrep-custom",
		RuleID:   "rules.security.CVE-2026-33810.exploit",
		Kind:     api.FindingKindSAST,
		Level:    api.FindingLevelUnknown,
		Message:  "Static match for CVE-2026-33810 vulnerable function call",
		Locations: []api.SecurityFindingLocation{{
			URI:       "pkg/auth.go",
			StartLine: 42,
		}},
	})

	if finding.Level != api.FindingLevelCritical {
		t.Fatalf("expected level Critical from embedded CVE, got %s", finding.Level)
	}
	resolvedAdv := scanfindings.Advisory(finding)
	if resolvedAdv == nil || resolvedAdv.SeveritySource != severity.SourceNVDCVSS {
		t.Fatalf("expected synthesized advisory with nvd.cvss, got %#v", resolvedAdv)
	}
	if resolvedAdv.OSVID != "CVE-2026-33810" {
		t.Fatalf("expected OSVID CVE-2026-33810, got %s", resolvedAdv.OSVID)
	}
	if len(resolvedAdv.CVSS) == 0 || resolvedAdv.CVSS[0].Score == nil || *resolvedAdv.CVSS[0].Score != 9.1 {
		t.Fatalf("expected CVSS score 9.1, got %#v", resolvedAdv.CVSS)
	}
}

func TestEnrichSecurityFindingLeavesUncatalogedAsUnknown(t *testing.T) {
	adv := advisory.BuildAdvisoryRef("GO-9999-0001")
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "lycaon-sca",
		RuleID:   "osv:GO-9999-0001",
		Kind:     api.FindingKindSCA,
		Level:    api.FindingLevelUnknown,
		Advisory: adv,
		Locations: []api.SecurityFindingLocation{{
			URI: "go.mod",
		}},
	})

	if finding.Level != api.FindingLevelUnknown {
		t.Fatalf("expected uncataloged finding to remain Unknown, got %s", finding.Level)
	}
}

func TestEnrichSecurityFindingPreservesNativeSeverity(t *testing.T) {
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "opengrep",
		RuleID:   "rules.go.sql-injection",
		Kind:     api.FindingKindSAST,
		Level:    api.FindingLevelMedium,
		Message:  "SQL query construction",
		Locations: []api.SecurityFindingLocation{{
			URI:       "db/query.go",
			StartLine: 10,
		}},
	})

	if finding.Level != api.FindingLevelMedium {
		t.Fatalf("native severity Medium was modified to %s", finding.Level)
	}
}
