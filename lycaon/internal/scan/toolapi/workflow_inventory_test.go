package toolapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type inventoryLedger struct {
	scanbase.ScanCoordinator
	scans   []api.CodeScan
	request scanbase.QueryRequest
}

func (l *inventoryLedger) ListByWorkflowRunID(context.Context, string) ([]api.CodeScan, error) {
	return l.scans, nil
}
func (l *inventoryLedger) Get(_ context.Context, id string) (*api.CodeScan, error) {
	for i := range l.scans {
		if l.scans[i].ID == id {
			return &l.scans[i], nil
		}
	}
	return nil, nil
}
func (l *inventoryLedger) Query(_ context.Context, req scanbase.QueryRequest) (*api.ScanQueryResponse, error) {
	l.request = req
	return &api.ScanQueryResponse{}, nil
}

func TestWorkflowInventoryPaginationUsesTheSameGroupsAsScanQuery(t *testing.T) {
	var findings []api.SecurityFinding
	for i := 0; i < 53; i++ {
		findings = append(findings, scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID: "sca", RuleID: "CVE-2026-12345", Kind: api.FindingKindSCA, Level: api.FindingLevelUnknown,
			Advisory: &api.AdvisoryRef{OSVID: "CVE-2026-12345", Package: &api.AdvisoryPackageRef{Name: fmt.Sprintf("pkg-%02d", i), Version: "1", Ecosystem: "go"}},
		}))
	}
	ledger := &inventoryLedger{scans: []api.CodeScan{
		{ID: "complete", Status: api.CodeScanStatusComplete, Findings: findings},
		{ID: "empty", Status: api.CodeScanStatusComplete},
		{ID: "failed", Status: api.CodeScanStatusFailed},
	}}
	raw, err := scanbase.WorkflowAdvisoryInventory(t.Context(), ledger, "run")
	testutil.FailErr(t, "inventory", err)
	var first struct {
		ScanIDs     []string `json:"scan_ids"`
		Groups      []scanfindings.FindingGroup
		TotalGroups int `json:"total_groups"`
		NextOffset  int `json:"next_offset"`
	}
	testutil.FailErr(t, "decode inventory", json.Unmarshal([]byte(raw), &first))
	if len(first.Groups) != 50 || first.TotalGroups != 53 || first.NextOffset != 50 || len(first.ScanIDs) != 2 {
		t.Fatalf("inventory=%s", raw)
	}
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "root"},
	}
	raw, err = runScanQuery(t.Context(), map[string]any{"scan_ids": first.ScanIDs, "view": "groups", "kind": "sca", "level": "unknown", "offset": first.NextOffset}, tctx, ledger, nil)
	testutil.FailErr(t, "next page", err)
	var page struct {
		Groups     []scanfindings.FindingGroup
		TotalMatch int `json:"total_match"`
		Truncated  bool
	}
	testutil.FailErr(t, "decode page", json.Unmarshal([]byte(raw), &page))
	if len(page.Groups) != 3 || page.TotalMatch != 53 || page.Truncated {
		t.Fatalf("page=%s", raw)
	}
	seen := map[string]bool{}
	for _, g := range append(first.Groups, page.Groups...) {
		if seen[g.ID] {
			t.Fatalf("duplicate group %s", g.ID)
		}
		seen[g.ID] = true
	}
	_, err = runScanQuery(t.Context(), map[string]any{"scan_ids": []string{"complete"}, "kind": "sca", "advisory_id": "CVE-2026-12345", "fingerprint": "fp"}, tctx, ledger, nil)
	testutil.FailErr(t, "filtered query", err)
	if ledger.request.Kind != "sca" || ledger.request.AdvisoryID != "CVE-2026-12345" || ledger.request.Fingerprint != "fp" {
		t.Fatalf("filters=%#v", ledger.request)
	}
	_, err = runScanQuery(t.Context(), map[string]any{"scan_ids": []string{"failed"}, "view": "groups"}, tctx, ledger, nil)
	if err == nil {
		t.Fatal("failed scan was represented as a completed empty inventory")
	}
}
