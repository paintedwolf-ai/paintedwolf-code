package search_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectScanFindingsKeysOnFingerprint(t *testing.T) {
	f := api.SecurityFinding{
		RuleID:  "osv:CVE-2024-1",
		Level:   api.FindingLevelHigh,
		Message: "test vuln",
		Locations: []api.SecurityFindingLocation{{
			URI:       "go.mod",
			StartLine: 1,
		}},
		Fingerprints: api.SecurityFindingFingerprints{Primary: "fp-deadbeef1234"},
		Tool:         api.ToolDescriptor{DriverID: "trivy", Name: "Trivy"},
		Properties: &api.SecurityFindingProperties{
			Lycaon: &api.SecurityFindingLycaonProperties{Kind: api.FindingKindSCA},
		},
	}
	rows := search.ProjectScanFindings("proj", "scan-1", []api.SecurityFinding{f}, "2026-01-01T00:00:00Z")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Handle != f.Fingerprints.Primary {
		t.Fatalf("handle = %q want %q", rows[0].Handle, f.Fingerprints.Primary)
	}
}
