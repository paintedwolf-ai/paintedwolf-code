package wiring

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/tools/toolstest"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
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
	wire "github.com/lycaon/lycaon/pkg/api"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

// A scan the runner completes reads back through the real server with the
// scanner's findings count and a response its OpenAPI declaration accepts.
func TestScanCompletedByRunnerReadsThroughServer(t *testing.T) {
	root := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(sqlDB, hub)
	outbox.Start(t.Context())
	t.Cleanup(func() { _ = outbox.Close() })

	projectDir := filepath.Join(root, "project")
	testutil.FailErr(t, "create project directory", os.MkdirAll(projectDir, 0o755))
	testutil.FailErr(t, "seed scan source", os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\n"), 0o600))
	projReg := project.NewSQLRegistry(sqlDB)
	proj, err := project.CreateWithRoot(t.Context(), projReg, projectDir)
	testutil.FailErr(t, "create project", err)
	scanEvents, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: proj.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project events", err)
	t.Cleanup(unsubscribe)

	scanStore := scan.NewSQLStore(sqlDB)
	scanStore.SetEventOutbox(outbox)
	coord := scantest.Coordinator(t, scanStore, nil)
	mock := &scan.MockScanner{Result: &scanoutput.Result{
		FindingsCount: 2,
		Findings: []wire.SecurityFinding{
			scanfindings.FixtureFinding("mock:first", wire.FindingLevelHigh, "first finding", "main.go", 1),
			scanfindings.FixtureFinding("mock:second", wire.FindingLevelMedium, "second finding", "main.go", 1),
		},
	}}
	reg := &scan.MockRegistry{Scanner: mock}
	// The host's coordinator selects the scanner's contract for the execution manifest.
	coord.Registry = reg
	ing := &scan.IngesterImpl{
		Module:                  scancfg.DefaultModuleConfig(),
		Budget:                  scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		BlockOn:                 []string{"error"},
		RecordWithoutDelegation: true,
	}
	runner := scanexecution.NewRunner(scanStore, reg, ing, scancfg.DefaultRunnerConfig(), &events.Publisher{Hub: hub})
	runner.Snapshots = coord.SnapshotStore()

	sessStore := store.NewSQL(sqlDB)
	mgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Limits: settings.DefaultSessionLimits()}, toolstest.NewRegistry())
	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{
		Core:  api.CoreDependencies{Store: sessStore, Projects: projReg, Sessions: mgr},
		Scans: api.ScansDependencies{ScanCoordinator: coord, ScannerRegistry: reg},
	}), nil, api.TestAPIToken)

	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	created, err := coord.Enqueue(t.Context(), scan.EnqueueRequest{
		ProjectDir: projectDir,
		Categories: []wire.ScanCategory{wire.ScanCategorySecurity},
		ScannerID:  mock.ID(),
		Trigger:    wire.ScanTriggerManual,
	})
	testutil.FailErr(t, "enqueue scan", err)
	awaitScanStatus(t, scanEvents, created.ID, wire.CodeScanStatusComplete)

	path := "/v1/projects/" + proj.ID + "/scans/" + created.ID
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	req.Header.Set("Authorization", api.TestAuthHeader())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
	}
	testutil.FailErr(t, "validate scan response", openapi.ValidateResponse(t.Context(), http.MethodGet, "/v1/projects/{id}/scans/{scan_id}",
		map[string]string{"id": proj.ID, "scan_id": created.ID}, rec.Code, rec.Header(), rec.Body.Bytes()))
	var got wire.CodeScan
	testutil.FailErr(t, "decode scan", json.Unmarshal(rec.Body.Bytes(), &got))
	if got.ID != created.ID || got.Status != wire.CodeScanStatusComplete || got.FindingsCount != 2 {
		t.Fatalf("scan = id %s status %s findings %d, want %s complete with 2", got.ID, got.Status, got.FindingsCount, created.ID)
	}
	running, err := scanStore.CountRunning(t.Context())
	testutil.FailErr(t, "count running scans", err)
	if running != 0 {
		t.Fatalf("running scans after completion = %d", running)
	}
}

// awaitScanStatus returns once the hub publishes want for scanID; any other
// terminal status fails the test.
func awaitScanStatus(t *testing.T, ch <-chan wire.EventEnvelope, scanID string, want wire.CodeScanStatus) {
	t.Helper()
	for {
		select {
		case envelope := <-ch:
			if envelope.Topic != wire.EventTopicScan {
				continue
			}
			var payload wire.CodeScanEvent
			testutil.FailErr(t, "decode scan event", json.Unmarshal(envelope.Data, &payload))
			if payload.ScanID != scanID {
				continue
			}
			switch payload.Status {
			case want:
				return
			case wire.CodeScanStatusPending, wire.CodeScanStatusRunning:
			default:
				t.Fatalf("scan %s reached %s, want %s", scanID, payload.Status, want)
			}
		case <-t.Context().Done():
			t.Fatalf("scan %s never published %s", scanID, want)
		}
	}
}
