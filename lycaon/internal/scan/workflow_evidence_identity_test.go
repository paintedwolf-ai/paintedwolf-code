package scan

import (
	"encoding/json"
	"testing"
	"time"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowInventoryKeepsBoundEmptyScanDespiteNewAmbientFindings(t *testing.T) {
	db := testdbfixture.Open(t, "identity.db")
	ledger := NewSQLStore(db)
	testdbseed.InsertWorkflowRun(t, db, "review-run", "review-session", testdbseed.DefaultProjectID)
	root := t.TempDir()
	bound := authorityScan("bound", "bound-assessment", root, "snapshot-before", "sca", api.ScanTargetFull, nil)
	bound.WorkflowRunID = "review-run"
	bound.CreatedAt = time.Now().Add(-time.Hour)
	testutil.FailErr(t, "insert bound scan", ledger.Insert(t.Context(), bound, nil, ""))
	completeNextScan(t, ledger)
	ambient := authorityScan("ambient", "ambient-assessment", root, "snapshot-after", "sca", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert newer ambient scan", ledger.Insert(t.Context(), ambient, nil, ""))
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{DriverID: "sca", RuleID: "advisory", Kind: api.FindingKindSCA, Level: api.FindingLevelUnknown})
	completeNextScan(t, ledger, finding)
	stored, err := ledger.Get(t.Context(), ambient.ID)
	testutil.FailErr(t, "load ambient evidence control", err)
	if stored == nil || len(scanfindings.GroupFindings(stored.Findings, 3)) != 1 {
		t.Fatal("ambient control must contain a persisted finding group")
	}
	for _, runID := range []string{"review-run", "absent-run"} {
		raw, err := WorkflowAdvisoryInventory(t.Context(), ledger, runID)
		testutil.FailErr(t, "project run inventory", err)
		var inventory struct {
			ScanIDs     []string `json:"scan_ids"`
			TotalGroups int      `json:"total_groups"`
		}
		testutil.FailErr(t, "decode inventory facts", json.Unmarshal([]byte(raw), &inventory))
		if inventory.TotalGroups != 0 {
			t.Fatalf("ambient findings leaked into %s", runID)
		}
		if runID == "review-run" {
			if len(inventory.ScanIDs) != 1 || inventory.ScanIDs[0] != bound.ID {
				t.Fatalf("empty completed scan lost its identity: %+v", inventory)
			}
		} else if len(inventory.ScanIDs) != 0 {
			t.Fatalf("missing run borrowed ambient scans: %+v", inventory)
		}
	}
}
