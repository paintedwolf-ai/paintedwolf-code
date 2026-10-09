//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestScanSummaryHTTPIntegration(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	findings := make([]wire.SecurityFinding, 0, 12)
	for i := 0; i < 8; i++ {
		findings = append(findings, scanfindings.FixtureFinding(
			"vendor.noise.rule",
			wire.FindingLevelInfo,
			"noise",
			fmt.Sprintf("noise_%d.rb", i),
			1,
		))
	}
	for i := 0; i < 4; i++ {
		findings = append(findings, scanfindings.FixtureFinding(
			"lycaon.ruby.sql-string-concat",
			wire.FindingLevelHigh,
			"sql",
			fmt.Sprintf("app_%d.rb", i),
			1,
		))
	}

	scanStore := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, scanStore, nil)
	mock := &scan.MockScanner{Result: &scanoutput.Result{
		FindingsCount: len(findings),
		Findings:      findings,
	}}
	reg := &scan.MockRegistry{Scanner: mock}
	ing := &scan.IngesterImpl{
		Module: scancfg.DefaultModuleConfig(),
		Budget: scancfg.NewFindingBudget(scancfg.AgentBudgetConfig{
			MaxGuidanceFindings:  500,
			MaxHintsPerInjection: 8,
			MinSeverity:          "warning",
			DedupeBy:             "rule_id+path",
		}),
		BlockOn:                 []string{"error"},
		RecordWithoutDelegation: true,
	}
	cfg := scancfg.DefaultRunnerConfig()
	runner := scanexecution.NewRunner(scanStore, reg, ing, cfg, nil)
	runner.Snapshots = coord.SnapshotStore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	sessStore := store.NewMemory()
	mgr := session.NewManager(sessStore, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	projects := project.NewMemoryRegistry()
	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{Core:api.CoreDependencies{
		Store: sessStore, Projects: projects, Sessions: mgr,},Scans:api.ScansDependencies{
		ScanCoordinator: coord, ScannerRegistry: reg,},}), nil, api.TestAPIToken)

	projectDir := t.TempDir()
	proj, err := project.CreateWithRoot(t.Context(), projects, projectDir)
	testutil.FailErr(t, "create scan project", err)
	created, err := coord.Enqueue(t.Context(), scan.EnqueueRequest{
		ProjectDir: projectDir,
		Categories: []wire.ScanCategory{wire.ScanCategorySecurity},
		ScannerID:  mock.ID(),
		Trigger:    wire.ScanTriggerManual,
	})
	testutil.FailErr(t, "enqueue scan fixture", err)
	scanURL := "/v1/projects/" + proj.ID + "/scans/" + created.ID

	var summary wire.CodeScan
	testutil.WaitFor(t, 2*time.Second, func() bool {
		sumReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, scanURL+"?view=summary", nil)
		sumReq.Header.Set("Authorization", api.TestAuthHeader())
		sumW := httptest.NewRecorder()
		srv.ServeHTTP(sumW, sumReq)
		if sumW.Code != http.StatusOK {
			t.Fatalf("GET summary status = %d", sumW.Code)
		}
		if err := json.Unmarshal(sumW.Body.Bytes(), &summary); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		return summary.Status == wire.CodeScanStatusComplete && len(summary.Guidance) > 0
	})

	if summary.Guidance[0].Code != "SCAN_SQL_INJECTION" {
		t.Fatalf("guidance = %#v", summary.Guidance)
	}
	if summary.FindingsCount <= len(summary.Guidance) {
		t.Fatalf("findings_count = %d guidance = %d want raw >> guidance", summary.FindingsCount, len(summary.Guidance))
	}
	if summary.FindingsByLevel == nil || summary.FindingsByLevel["high"] == 0 {
		t.Fatalf("findings_by_level = %#v", summary.FindingsByLevel)
	}
	if summary.FindingsStored == 0 {
		t.Fatalf("findings_stored = %d", summary.FindingsStored)
	}
	if summary.AgentBudget == nil || summary.ScanScope == "" {
		t.Fatalf("agent_budget=%#v scan_scope=%q", summary.AgentBudget, summary.ScanScope)
	}
	if len(summary.Findings) != 0 || summary.Result != nil {
		t.Fatalf("summary view leaked audit fields: findings=%d result=%v", len(summary.Findings), summary.Result)
	}

	fullReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, scanURL+"?view=full", nil)
	fullReq.Header.Set("Authorization", api.TestAuthHeader())
	fullW := httptest.NewRecorder()
	srv.ServeHTTP(fullW, fullReq)
	var full wire.CodeScan
	if err := json.Unmarshal(fullW.Body.Bytes(), &full); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(full.Findings) == 0 {
		t.Fatal("expected complete findings in full view")
	}

	badViewReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, scanURL+"?view=invalid_view", nil)
	badViewReq.Header.Set("Authorization", api.TestAuthHeader())
	badViewW := httptest.NewRecorder()
	srv.ServeHTTP(badViewW, badViewReq)
	if badViewW.Code != http.StatusBadRequest {
		t.Fatalf("invalid view status = %d body = %s", badViewW.Code, badViewW.Body.String())
	}
}
