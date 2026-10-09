package toolapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
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

// Paging echoes the inventory revision; a page requested against a changed
// inventory is refused rather than spliced onto a different group list.
func TestScanQueryGroupsRefuseAStaleInventoryRevision(t *testing.T) {
	ledger := &inventoryLedger{scans: []api.CodeScan{{ID: "complete", Status: api.CodeScanStatusComplete}}}
	tctx := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}, ActiveRootID: "root"}}
	raw, err := runScanQuery(t.Context(), map[string]any{"scan_ids": []string{"complete"}, "view": "groups"}, tctx, ledger, nil)
	testutil.FailErr(t, "first page", err)
	var page struct {
		Revision string `json:"inventory_revision"`
	}
	testutil.FailErr(t, "decode page", json.Unmarshal([]byte(raw), &page))
	want, err := scanfindings.InventoryRevision(ledger.scans)
	testutil.FailErr(t, "inventory revision", err)
	if page.Revision != want {
		t.Fatalf("page revision = %q", page.Revision)
	}
	_, err = runScanQuery(t.Context(), map[string]any{"scan_ids": []string{"complete"}, "view": "groups", "inventory_revision": page.Revision}, tctx, ledger, nil)
	testutil.FailErr(t, "page at the current revision", err)

	var reject *toolrejection.ToolReject
	_, err = runScanQuery(t.Context(), map[string]any{"scan_ids": []string{"complete"}, "view": "groups", "inventory_revision": "stale"}, tctx, ledger, nil)
	if !errors.As(err, &reject) || reject.Code != "SCAN_INVENTORY_STALE" || reject.Data["inventory_revision"] != page.Revision {
		t.Fatalf("stale revision err = %v", err)
	}
	for field, args := range map[string]map[string]any{
		"selector":           {"scan_ids": []string{"complete"}, "selector": map[string]any{"scanner": "sca", "paths": []any{"x"}}},
		"inventory_revision": {"scan_ids": []string{"complete"}, "inventory_revision": page.Revision},
	} {
		_, err := runScanQuery(t.Context(), args, tctx, ledger, nil)
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" || reject.Data["field"] != field {
			t.Fatalf("%s outside its view: err = %v", field, err)
		}
	}
}

type recordingAccounting struct{ sessionID string }

func (r *recordingAccounting) QueryWorkflowInventory(_ context.Context, sessionID string, _ map[string]any) (string, error) {
	r.sessionID = sessionID
	return `{"view":"accounting"}`, nil
}

type noFullScans struct{}

func (noFullScans) RequestFull(context.Context, string, []string, api.ScanTrigger, scanbase.FullScanContext) (scanbase.FullPass, error) {
	return scanbase.FullPass{}, errors.New("not used")
}

// The accounting view answers from the workflow's accepted evidence, not the
// scan ledger; without a workflow source it fails instead of guessing.
func TestScanQueryAccountingViewReadsWorkflowAccounting(t *testing.T) {
	ledger := &inventoryLedger{}
	accounting := &recordingAccounting{}
	scanners := &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterScanTools(reg, ledger, scanners, noFullScans{}, nil, nil, accounting))
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}, ActiveRootID: "root"}}
	out, err := reg.Run(t.Context(), toolScanQuery, map[string]any{"scan_ids": []any{"s"}, "view": "accounting"}, tctx)
	testutil.FailErr(t, "accounting view", err)
	if out != `{"view":"accounting"}` || accounting.sessionID != "session" || ledger.request.ScanID != "" {
		t.Fatalf("out = %s session = %q ledger request = %+v", out, accounting.sessionID, ledger.request)
	}

	unwired := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register unwired", RegisterScanTools(unwired, ledger, scanners, noFullScans{}, nil, nil, nil))
	if _, err := unwired.Run(t.Context(), toolScanQuery, map[string]any{"scan_ids": []any{"s"}, "view": "accounting"}, tctx); err == nil {
		t.Fatal("accounting view answered without workflow accounting")
	}
}
