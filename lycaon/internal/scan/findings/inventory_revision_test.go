package findings

import (
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestInventoryRevisionIsOrderIndependentAndCountsEveryFinding(t *testing.T) {
	f := BuildSecurityFinding(FindingBuildOpts{DriverID: "scanner", RuleID: "rule", Level: api.FindingLevelHigh})
	first := api.CodeScan{ID: "first", Findings: []api.SecurityFinding{f}}
	second := api.CodeScan{ID: "second"}
	before := InventoryRevision([]api.CodeScan{first, second})
	if before != InventoryRevision([]api.CodeScan{second, first}) {
		t.Fatal("scan order changed inventory revision")
	}
	first.Findings = append(first.Findings, f)
	if before == InventoryRevision([]api.CodeScan{first, second}) {
		t.Fatal("additional row in same group did not invalidate revision")
	}
}
