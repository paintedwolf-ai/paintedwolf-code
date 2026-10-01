package findings_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/advisory"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/scan/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMergeByAdvisoryCollapsesSameCVE(t *testing.T) {
	cve := "CVE-2024-9999"
	trivy := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "trivy",
		RuleID:   advisory.RuleID(advisory.BuildAdvisoryRef(cve)),
		Level:    api.FindingLevelMedium,
		Message:  "trivy row",
		Kind:     api.FindingKindSCA,
		Locations: []api.SecurityFindingLocation{{
			URI: "debian:12",
		}},
		Advisory: testfixture.AdvisoryWithPackage(cve, "example/module", "1.2.3", "gomod"),
	})
	scalibr := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "lycaon-sca",
		RuleID:   advisory.RuleID(advisory.BuildAdvisoryRef(cve)),
		Level:    api.FindingLevelHigh,
		Message:  "scalibr row",
		Kind:     api.FindingKindSCA,
		Locations: []api.SecurityFindingLocation{{
			URI: "go.mod",
		}},
		Advisory: testfixture.AdvisoryWithPackage(cve, "example/module", "1.2.3", "go"),
	})
	merged := scanfindings.MergeByAdvisory([]api.SecurityFinding{trivy, scalibr})
	if len(merged) != 1 {
		t.Fatalf("merged = %d want 1", len(merged))
	}
	if merged[0].Level != api.FindingLevelHigh {
		t.Fatalf("level = %q want high", merged[0].Level)
	}
	if scanfindings.PrimaryURI(merged[0]) != "go.mod" {
		t.Fatalf("uri = %q want go.mod", scanfindings.PrimaryURI(merged[0]))
	}
	if merged[0].Properties == nil || merged[0].Properties.Lycaon == nil || len(merged[0].Properties.Lycaon.Sources) != 2 {
		t.Fatalf("sources = %#v", merged[0].Properties)
	}
}

func TestMergeByAdvisoryKeepsDifferentPackages(t *testing.T) {
	cve := "CVE-2024-9999"
	first := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "trivy", RuleID: advisory.RuleID(advisory.BuildAdvisoryRef(cve)), Level: api.FindingLevelHigh,
		Kind: api.FindingKindSCA, Advisory: testfixture.AdvisoryWithPackage(cve, "package-a", "1.0.0", "npm"),
	})
	second := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "scalibr", RuleID: advisory.RuleID(advisory.BuildAdvisoryRef(cve)), Level: api.FindingLevelHigh,
		Kind: api.FindingKindSCA, Advisory: testfixture.AdvisoryWithPackage(cve, "package-b", "1.0.0", "npm"),
	})
	if got := scanfindings.MergeByAdvisory([]api.SecurityFinding{first, second}); len(got) != 2 {
		t.Fatalf("merged = %d want 2 distinct affected packages", len(got))
	}
}

func TestFindingBudgetDedupeByFingerprint(t *testing.T) {
	budget := scancfg.NewFindingBudget(scancfg.AgentBudgetConfig{
		MaxGuidanceFindings: 10,
		MinSeverity:         "info",
		DedupeBy:            "fingerprint_primary",
	})
	dup := scanfindings.FixtureFinding("r1", api.FindingLevelHigh, "a", "src/a.go", 1)
	dup2 := dup
	dup2.Locations = []api.SecurityFindingLocation{{URI: "src/b.go", StartLine: 2}}
	scanfindings.RefreshFingerprint(&dup2)
	in := []api.SecurityFinding{dup, dup2}
	out := budget.Apply(in, nil)
	if len(out.Findings) != 2 {
		t.Fatalf("distinct fingerprints = %d want 2", len(out.Findings))
	}
	dup3 := dup
	out = budget.Apply([]api.SecurityFinding{dup, dup3}, nil)
	if len(out.Findings) != 1 {
		t.Fatalf("same fingerprint = %d want 1", len(out.Findings))
	}
}
