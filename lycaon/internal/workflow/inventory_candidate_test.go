package workflow

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/conditions"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestInventoryPreviewChecksEveryLocation(t *testing.T) {
	selector := scanfindings.SetAside{Scanner: "sast", Paths: []string{"**/*_test.go"}}
	cases := []struct {
		name            string
		paths           []string
		selected, mixed bool
	}{
		{"fixture", []string{"internal/a_test.go"}, true, false},
		{"mixed", []string{"internal/a_test.go", "internal/live.go"}, false, true},
		{"locationless", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			group := scanfindings.InventoryGroup{Scanner: "sast", Paths: tc.paths}
			if selector.Selects(group) != tc.selected || selectorPartlyMatches(selector, group) != tc.mixed {
				t.Fatal("selector preview misclassified complete location set")
			}
		})
	}
}

func TestInventoryAccountingRequiresBoundScansAndPreservesRevision(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	scans := []api.CodeScan{{ID: "scan", ScannerID: "sast", Status: api.CodeScanStatusComplete, Findings: []api.SecurityFinding{scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{DriverID: "sast", RuleID: "rule", Level: api.FindingLevelHigh})}}}
	mgr.Inventory = fakeInventory{run: scans}
	raw, err := mgr.QueryWorkflowInventory(t.Context(), run.SessionID, map[string]any{"scan_ids": []string{"scan"}, "view": "accounting"})
	testutil.FailErr(t, "query accounting", err)
	var got struct {
		Revision    string `json:"inventory_revision"`
		Unaccounted int    `json:"unaccounted_groups"`
		Candidate   string `json:"candidate_state"`
	}
	testutil.FailErr(t, "decode accounting", json.Unmarshal([]byte(raw), &got))
	if got.Revision != scanfindings.InventoryRevision(scans) || got.Unaccounted != 1 || got.Candidate != "absent" {
		t.Fatalf("incorrect accounting: %s", raw)
	}
	for _, args := range []map[string]any{
		{"scan_ids": []any{"unbound"}},
		{"scan_ids": []any{"scan"}, "inventory_revision": "stale"},
		{"scan_ids": []any{"scan"}, "path": "subset.go"},
	} {
		if _, err := mgr.QueryWorkflowInventory(t.Context(), run.SessionID, args); err == nil {
			t.Fatalf("accepted invalid inventory query: %+v", args)
		}
	}
}
