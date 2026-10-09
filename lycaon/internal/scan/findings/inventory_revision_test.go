package findings

import (
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInventoryRevisionIsOrderIndependentAndCountsEveryFinding(t *testing.T) {
	f := BuildSecurityFinding(FindingBuildOpts{DriverID: "scanner", RuleID: "rule", Level: api.FindingLevelHigh})
	first := api.CodeScan{ID: "first", Findings: []api.SecurityFinding{f}}
	second := api.CodeScan{ID: "second"}
	before, err := InventoryRevision([]api.CodeScan{first, second})
	testutil.FailErr(t, "revision", err)
	reordered, err := InventoryRevision([]api.CodeScan{second, first})
	testutil.FailErr(t, "reordered revision", err)
	if before != reordered {
		t.Fatal("scan order changed inventory revision")
	}
	first.Findings = append(first.Findings, f)
	grown, err := InventoryRevision([]api.CodeScan{first, second})
	testutil.FailErr(t, "grown revision", err)
	if before == grown {
		t.Fatal("additional row in same group did not invalidate revision")
	}
}

func TestInventoryRevisionRefusesAnUnencodableFinding(t *testing.T) {
	score := math.NaN()
	f := BuildSecurityFinding(FindingBuildOpts{DriverID: "scanner", RuleID: "rule", Level: api.FindingLevelHigh,
		Advisory: &api.AdvisoryRef{OSVID: "GO-0000-0000", CVSS: []api.AdvisoryCVSS{{Type: "CVSS_V3", Score: &score}}}})
	if revision, err := InventoryRevision([]api.CodeScan{{ID: "scan", Findings: []api.SecurityFinding{f}}}); err == nil {
		t.Fatalf("unencodable finding produced revision %q", revision)
	}
}
