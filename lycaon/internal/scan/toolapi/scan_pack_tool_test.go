package toolapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/testfixture"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanPackToolReturnsEnqueueReceipt(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")

	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, sqlDB, "sess-1", testdbseed.DefaultProjectID)

	projectDir := t.TempDir()
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, staticHead{sha: "abc"})
	toolReg := tools.NewDefaultRegistry()
	scannerReg := &fanOutMockRegistry{scanners: []scanbase.CodeScanner{
		&scanbase.MockScanner{
			IDVal:        "lycaon-sca",
			CategoryList: []api.ScanCategory{api.ScanCategorySCA, api.ScanCategorySecurity},
			Result:       &scanoutput.Result{FindingsCount: 54},
		},
		&scanbase.MockScanner{
			IDVal:        "lycaon-secrets",
			CategoryList: []api.ScanCategory{api.ScanCategorySecret, api.ScanCategorySecurity},
			Result:       &scanoutput.Result{FindingsCount: 0},
		},
		&scanbase.MockScanner{
			IDVal:        "lycaon-sast",
			CategoryList: []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
			Result:       &scanoutput.Result{FindingsCount: 0},
		},
	}}
	if err := scantoolapi.RegisterScanTools(toolReg, coord, scannerReg, scancadence.New(scanbase.StoreFromCoordinator(coord), coord, scannerReg, nil, scancfg.DefaultGatesConfig(), nil), nil, nil, nil); err != nil {
		testutil.FailErr(t, "scan.RegisterScanTools failed", err)
	}

	out, err := toolReg.Run(context.Background(), "scan_pack", map[string]any{
		"categories": []any{"all"},
	}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "reg.Run failed", err)

	var payload scantoolapi.ScanPackToolResult
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if payload.Status != api.CodeScanStatusPending {
		t.Fatalf("status = %q body=%s want pending", payload.Status, out)
	}
	if len(payload.ScanIDs) != 3 {
		t.Fatalf("scan_ids = %v, want 3 engines", payload.ScanIDs)
	}
	if !strings.Contains(payload.Message, "scan_done condition") {
		t.Fatalf("message = %q want scan_done condition hint", payload.Message)
	}

	out, err = toolReg.Run(context.Background(), "scan_pack", map[string]any{
		"categories": []any{"all"}, "completion": "summary", "timeout_ms": 25,
	}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "timed scan_pack", err)
	testutil.FailErr(t, "decode timed scan_pack", json.Unmarshal([]byte(out), &payload))
	if payload.Status != api.CodeScanStatusPending || !payload.WaitTimedOut {
		t.Fatalf("timed receipt = %#v", payload)
	}
	if !strings.Contains(payload.Message, "continue in the background") {
		t.Fatalf("message = %q want continuation fact", payload.Message)
	}
}

func TestScanPackToolCanWaitForSummary(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")

	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, sqlDB, "sess-1", testdbseed.DefaultProjectID)

	projectDir := t.TempDir()
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, staticHead{sha: "abc"})
	scannerReg := &fanOutMockRegistry{scanners: []scanbase.CodeScanner{
		&scanbase.MockScanner{
			IDVal: "scanner-a", CategoryList: []api.ScanCategory{api.ScanCategorySecurity},
			Result: &scanoutput.Result{FindingsCount: 0},
		},
	}}
	toolReg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterScanTools", scantoolapi.RegisterScanTools(toolReg, coord, scannerReg, scancadence.New(scanbase.StoreFromCoordinator(coord), coord, scannerReg, nil, scancfg.DefaultGatesConfig(), nil), nil, nil, nil))

	runnerConfig := scancfg.DefaultRunnerConfig()
	runner := scanexecution.NewRunner(store, scannerReg, scanbase.NoopIngester{}, runnerConfig, nil)
	runner.Snapshots = coord.SnapshotStore()
	runCtx, cancel := context.WithCancel(t.Context())
	runnerDone := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-runnerDone:
		case <-time.After(2 * time.Second):
			t.Error("scan runner did not stop")
		}
	})
	go func() {
		defer close(runnerDone)
		_ = runner.Run(runCtx)
	}()

	out, err := toolReg.Run(t.Context(), "scan_pack", map[string]any{
		"categories": []any{"security"}, "completion": "summary", "timeout_ms": 3000,
	}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "scan_pack summary", err)
	var payload scantoolapi.ScanPackToolResult
	testutil.FailErr(t, "decode scan_pack summary", json.Unmarshal([]byte(out), &payload))
	if payload.Status != api.CodeScanStatusComplete || payload.WaitTimedOut {
		t.Fatalf("summary receipt = %#v", payload)
	}
	if len(payload.ScanIDs) != 1 || !strings.Contains(payload.Message, "zero findings") {
		t.Fatalf("summary receipt = %#v", payload)
	}
}

func TestScanQueryAggregatesPackAcrossEnginesByPackage(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	projectDir := t.TempDir()
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	toolReg := tools.NewDefaultRegistry()
	registry := &fanOutMockRegistry{scanners: []scanbase.CodeScanner{
		&scanbase.MockScanner{IDVal: "scanner-a", CategoryList: []api.ScanCategory{api.ScanCategorySCA}},
	}}
	testutil.FailErr(t, "RegisterScanTools", scantoolapi.RegisterScanTools(toolReg, coord, registry, scancadence.New(scanbase.StoreFromCoordinator(coord), coord, registry, nil, scancfg.DefaultGatesConfig(), nil), nil, nil, nil))

	ids := []string{"pack-scan-a", "pack-scan-b"}
	for index, id := range ids {
		finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID: []string{"scanner-a", "scanner-b"}[index],
			RuleID:   advisory.RuleID(advisory.BuildAdvisoryRef("CVE-2026-1234")), Level: api.FindingLevelHigh,
			Kind:     api.FindingKindSCA,
			Advisory: testfixture.AdvisoryWithPackage("CVE-2026-1234", "example/module", "1.2.3", "go"),
		})
		record := api.CodeScan{
			ID: id, CanonicalPath: projectDir, Categories: []api.ScanCategory{api.ScanCategorySCA},
			ScannerID: []string{"scanner-a", "scanner-b"}[index], Status: api.CodeScanStatusComplete,
			SourceSnapshotID: "snapshot-1", Trigger: api.ScanTriggerScanPack,
		}
		testutil.FailErr(t, "insert pack member", store.Insert(t.Context(), record, []string{"go.mod"}, ""))
		testutil.FailErr(t, "save pack ingest", store.SaveIngest(t.Context(), id, evidence.Record{
			Artifacts: map[string]any{
				"findings": []api.SecurityFinding{finding}, "findings_count": 1,
				"findings_stored": 1, "guidance": []api.ScanGuidanceSummary{},
			},
		}))
	}

	out, err := toolReg.Run(t.Context(), "scan_query", map[string]any{
		"scan_ids": []any{ids[0], ids[1]},
	}, scanToolContext("session", projectDir))
	testutil.FailErr(t, "scan_query pack", err)
	var response api.ScanQueryResponse
	testutil.FailErr(t, "decode pack query", json.Unmarshal([]byte(out), &response))
	if response.TotalMatch != 1 || len(response.Findings) != 1 {
		t.Fatalf("aggregate response = %#v", response)
	}
	sources := response.Findings[0].Properties.Lycaon.Sources
	if len(sources) != 2 {
		t.Fatalf("sources = %v want two engines", sources)
	}
}

func TestWaitForScanIDTimesOutWhilePending(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")

	sqlDB := testdbfixture.Open(t, "store.db")

	store := scanbase.NewSQLStore(sqlDB)
	projectDir := t.TempDir()
	if err := store.Insert(context.Background(), api.CodeScan{
		ID:            "pending-1",
		CanonicalPath: projectDir,
		Categories:    []api.ScanCategory{api.ScanCategorySCA},
		Status:        api.CodeScanStatusPending,
		CreatedAt:     time.Now().UTC(),
	}, nil, ""); err != nil {
		testutil.FailErr(t, "insert pending scan", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := scanbase.WaitForScanID(ctx, store, "pending-1", 0)
	if err == nil {
		t.Fatal("expected timeout")
	}
}

func TestWaitForScanIDFollowsSnapshotReplacement(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := scanbase.NewSQLStore(sqlDB)
	now := time.Now().UTC()
	projectDir := t.TempDir()
	for _, rec := range []api.CodeScan{
		{ID: "superseded-1", CanonicalPath: projectDir, Categories: []api.ScanCategory{api.ScanCategorySecurity}, Status: api.CodeScanStatusPending, CreatedAt: now},
		{ID: "replacement-1", CanonicalPath: projectDir, Categories: []api.ScanCategory{api.ScanCategorySecurity}, Status: api.CodeScanStatusComplete, CreatedAt: now},
	} {
		testutil.FailErr(t, "insert scan "+rec.ID, store.Insert(t.Context(), rec, nil, ""))
	}
	won, err := store.MarkPendingSuperseded(t.Context(), "superseded-1", "replacement-1")
	testutil.FailErr(t, "supersede scan", err)
	if !won {
		t.Fatal("scan superseded-1 was not pending")
	}
	got, err := scanbase.WaitForScanID(t.Context(), store, "superseded-1", time.Millisecond)
	testutil.FailErr(t, "wait for replacement", err)
	if got == nil || got.ID != "replacement-1" || got.Status != api.CodeScanStatusComplete {
		t.Fatalf("replacement result = %+v", got)
	}
}

func scanToolContext(sessionID, dir string, agents ...string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID},
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID: "r1"},
	}
	if len(agents) > 0 {
		tctx.Identity.Agent = agents[0]
	}
	return tctx
}
