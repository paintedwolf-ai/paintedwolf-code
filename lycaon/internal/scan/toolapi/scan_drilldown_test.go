package toolapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func registerNativeScanTools(t *testing.T) (*tools.DefaultRegistry, scanbase.ScanCoordinator, *scanbase.SQLStore, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "scan-drilldown.db")

	projectDir := t.TempDir()
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	reg := tools.NewDefaultRegistry()
	if err := scantoolapi.RegisterScanTools(reg, coord, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, scancadence.New(scanbase.StoreFromCoordinator(coord), coord, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, nil, scancfg.DefaultGatesConfig(), nil), nil, nil); err != nil {
		testutil.FailErr(t, "RegisterScanTools failed", err)
	}
	return reg, coord, store, projectDir
}

func TestNativeScanListReturnsProjectScans(t *testing.T) {
	reg, _, store, projectDir := registerNativeScanTools(t)
	if err := store.Insert(context.Background(), wire.CodeScan{
		ID:            "scan-sca-1",
		CanonicalPath: projectDir,
		ScannerID:     "lycaon-sca",
		Categories:    []wire.ScanCategory{wire.ScanCategorySCA},
		Status:        wire.CodeScanStatusComplete,
		FindingsCount: 3,
		CreatedAt:     time.Now().UTC(),
	}, nil, ""); err != nil {
		t.Fatal(err)
	}

	out, err := reg.Run(context.Background(), "scan_list", map[string]any{"limit": 10}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "scan_list failed", err)

	var listed scantoolapi.ListScansResponse
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		testutil.FailErr(t, "decode scan_list", err)
	}
	if listed.Count != 1 || len(listed.Scans) != 1 || listed.Scans[0].ID != "scan-sca-1" {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestNativeScanListOmitsGuidance(t *testing.T) {
	reg, _, store, projectDir := registerNativeScanTools(t)
	if err := store.Insert(context.Background(), wire.CodeScan{
		ID:            "scan-sca-guide",
		CanonicalPath: projectDir,
		ScannerID:     "lycaon-sca",
		Categories:    []wire.ScanCategory{wire.ScanCategorySCA},
		Status:        wire.CodeScanStatusComplete,
		FindingsCount: 1,
		CreatedAt:     time.Now().UTC(),
	}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIngest(context.Background(), "scan-sca-guide", evidence.Record{
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"findings": []wire.SecurityFinding{},
			"guidance": []wire.ScanGuidanceSummary{{
				Code:    "SCAN_SQL_INJECTION",
				Message: "example guidance that must not bloat scan_list",
			}},
			"findings_count":  1,
			"findings_stored": 0,
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := reg.Run(context.Background(), "scan_list", map[string]any{"limit": 10}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "scan_list failed", err)
	var listed scantoolapi.ListScansResponse
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		testutil.FailErr(t, "decode scan_list", err)
	}
	if listed.Count != 1 || len(listed.Scans[0].Guidance) != 0 {
		t.Fatalf("scan_list must omit guidance; got %+v", listed.Scans[0])
	}
}

func TestNativeScanListEmptyHint(t *testing.T) {
	reg, _, _, projectDir := registerNativeScanTools(t)

	out, err := reg.Run(context.Background(), "scan_list", nil, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "scan_list failed", err)

	var listed scantoolapi.ListScansResponse
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		testutil.FailErr(t, "decode scan_list", err)
	}
	if listed.Count != 0 || listed.HintCode != "SCAN_LIST_EMPTY" || listed.Hint == "" {
		t.Fatalf("empty list = %+v", listed)
	}
}

func TestNativeScanSummaryAndErrors(t *testing.T) {
	reg, _, store, projectDir := registerNativeScanTools(t)
	if err := store.Insert(context.Background(), wire.CodeScan{
		ID:            "scan-sca-1",
		CanonicalPath: projectDir,
		ScannerID:     "lycaon-sca",
		Categories:    []wire.ScanCategory{wire.ScanCategorySCA},
		Status:        wire.CodeScanStatusComplete,
		FindingsCount: 3,
		CreatedAt:     time.Now().UTC(),
	}, nil, ""); err != nil {
		t.Fatal(err)
	}
	tctx := scanToolContext("sess-1", projectDir)

	out, err := reg.Run(context.Background(), "scan_summary", map[string]any{"scan_ids": []any{"scan-sca-1"}}, tctx)
	testutil.FailErr(t, "scan_summary failed", err)
	var rec wire.CodeScan
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		testutil.FailErr(t, "decode summary", err)
	}
	if rec.ID != "scan-sca-1" {
		t.Fatalf("summary id = %q", rec.ID)
	}

	_, err = reg.Run(context.Background(), "scan_summary", map[string]any{"scan_ids": []any{"missing-scan"}}, tctx)
	if err == nil {
		t.Fatal("expected error for missing scan")
	}
	body := err.Error()
	if body == "" || strings.Contains(body, "sql:") {
		t.Fatalf("missing scan error = %q want structured reject without sql leak", body)
	}
	if !strings.Contains(body, scanbase.DrilldownRejectNotFound) {
		t.Fatalf("missing scan error = %q want %s", body, scanbase.DrilldownRejectNotFound)
	}

	_, err = reg.Run(context.Background(), "scan_query", map[string]any{"scan_ids": []any{"missing-scan"}}, tctx)
	if err == nil {
		t.Fatal("expected error for missing scan query")
	}
	if body := err.Error(); strings.Contains(body, "sql:") {
		t.Fatalf("missing scan query error leaked sql: %q", body)
	}
	if !strings.Contains(err.Error(), scanbase.DrilldownRejectNotFound) {
		t.Fatalf("missing scan query error = %q want %s", err.Error(), scanbase.DrilldownRejectNotFound)
	}

	_, err = reg.Run(context.Background(), "scan_summary", map[string]any{
		"scan_ids": []any{"scan-sca-1"},
		"view":     "invalid_view",
	}, tctx)
	if err == nil || !strings.Contains(err.Error(), "invalid view") {
		t.Fatalf("invalid view result = %v", err)
	}
}

func TestNativeScanSummaryRequiresOneSelector(t *testing.T) {
	reg, _, _, projectDir := registerNativeScanTools(t)
	tctx := scanToolContext("sess-1", projectDir)

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing", want: "scan_ids or pass_id is required"},
		{name: "blank pass", args: map[string]any{"pass_id": " "}, want: "scan_ids or pass_id is required"},
		{name: "both selectors", args: map[string]any{"scan_ids": []any{"scan-1"}, "pass_id": "pass-1"}, want: "give scan_ids or pass_id, not both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Run(t.Context(), "scan_summary", tc.args, tctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid selector = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNativeScanQueryRequiresScanIDs(t *testing.T) {
	reg, _, _, projectDir := registerNativeScanTools(t)
	tctx := scanToolContext("sess-1", projectDir)

	_, err := reg.Run(context.Background(), "scan_query", nil, tctx)
	if err == nil || !strings.Contains(err.Error(), "scan_ids is required") {
		t.Fatalf("missing scan_ids = %v, want required error", err)
	}
}

func TestNativeScanQueryOnCompleteScan(t *testing.T) {
	reg, _, store, projectDir := registerNativeScanTools(t)
	if err := store.Insert(context.Background(), wire.CodeScan{
		ID:            "scan-sca-1",
		CanonicalPath: projectDir,
		ScannerID:     "lycaon-sca",
		Categories:    []wire.ScanCategory{wire.ScanCategorySCA},
		Status:        wire.CodeScanStatusComplete,
		CreatedAt:     time.Now().UTC(),
	}, nil, ""); err != nil {
		t.Fatal(err)
	}

	out, err := reg.Run(context.Background(), "scan_query", map[string]any{"scan_ids": []any{"scan-sca-1"}}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "scan_query failed", err)

	var resp wire.ScanQueryResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		testutil.FailErr(t, "decode scan_query", err)
	}
	if resp.ScanID != "scan-sca-1" {
		t.Fatalf("scan_id = %q", resp.ScanID)
	}
}

func TestNativeScanListRequiresProjectRoot(t *testing.T) {
	reg, _, _, _ := registerNativeScanTools(t)
	tctx := tools.ToolContext{SessionID: "sess-1"}

	_, err := reg.Run(context.Background(), "scan_list", nil, tctx)
	if err == nil || !strings.Contains(err.Error(), "no project_dir") {
		t.Fatalf("missing project root = %v", err)
	}
}
