package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestOpenAPIScansRealServer(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	root := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))

	scanStore := scan.NewSQLStore(sqlDB)
	coord := scantest.Coordinator(t, scanStore, nil)
	mock := &scan.MockScanner{Result: &scanoutput.Result{
		FindingsCount: 2,
		Findings: []wire.SecurityFinding{
			scanfindings.FixtureFinding("mock:first", wire.FindingLevelHigh, "first finding", "main.go", 1),
			scanfindings.FixtureFinding("mock:second", wire.FindingLevelMedium, "second finding", "main.go", 1),
		},
	}}
	reg := &scan.MockRegistry{Scanner: mock}
	ing := &scan.IngesterImpl{
		Module:                  scancfg.DefaultModuleConfig(),
		Budget:                  scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		BlockOn:                 []string{"error"},
		RecordWithoutDelegation: true,
	}
	cfg := scancfg.DefaultRunnerConfig()
	runner := scanexecution.NewRunner(scanStore, reg, ing, cfg, nil)
	runner.Snapshots = coord.SnapshotStore()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Run(ctx)
	}()
	var stopOnce sync.Once
	stopRunner := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Errorf("scan runner did not stop")
			}
		})
	}
	t.Cleanup(stopRunner)

	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		contractcheck.FailErr(t, "create directory", err)
	}
	testutil.FailErr(t, "seed scan source", os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\n"), 0o600))

	projReg := project.NewMemoryRegistry()
	proj, err := projReg.Create(t.Context(), project.CreateParams{
		Roots: []project.AttachRootParams{{Path: projectDir}},
	})
	testutil.FailErr(t, "create project", err)

	sessStore := store.NewMemory()
	mockLLM := llm.NewMockProvider(nil)
	mgr := session.NewManager(sessStore, mockLLM, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{
		Store: sessStore, Projects: projReg, Sessions: mgr,
		ScanCoordinator: coord, ScannerRegistry: reg,
	}), nil, api.TestAPIToken)

	created, err := coord.Enqueue(t.Context(), scan.EnqueueRequest{
		ProjectDir: projectDir,
		Categories: []wire.ScanCategory{wire.ScanCategorySecurity},
		ScannerID:  mock.ID(),
		Trigger:    wire.ScanTriggerManual,
	})
	testutil.FailErr(t, "enqueue scan fixture", err)
	// Enqueue rereads the committed scan, which the runner may already have finished.
	if created.Status != wire.CodeScanStatusPending && created.Status != wire.CodeScanStatusRunning && created.Status != wire.CodeScanStatusComplete {
		t.Fatalf("created status = %q", created.Status)
	}

	testutil.WaitFor(t, 2*time.Second, func() bool {
		getReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/projects/"+proj.ID+"/scans/"+created.ID, nil)
		getReq.Header.Set("Authorization", api.TestAuthHeader())
		getW := httptest.NewRecorder()
		srv.ServeHTTP(getW, getReq)
		if getW.Code != http.StatusOK {
			t.Fatalf("GET status = %d", getW.Code)
		}
		var got wire.CodeScan
		if err := json.Unmarshal(getW.Body.Bytes(), &got); err != nil {
			contractcheck.FailErr(t, "unmarshal JSON document", err)
		}
		if got.Status != wire.CodeScanStatusPending && got.Status != wire.CodeScanStatusRunning && got.Status != wire.CodeScanStatusComplete {
			t.Fatalf("scan reached terminal status %q: %s", got.Status, got.Error)
		}
		return got.Status == wire.CodeScanStatusComplete && got.FindingsCount == 2
	})

	testutil.WaitFor(t, time.Second, func() bool {
		running, err := scanStore.CountRunning(context.Background())
		return err == nil && running == 0
	})
	stopRunner()
}
