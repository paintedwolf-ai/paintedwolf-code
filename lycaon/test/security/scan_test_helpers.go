package security

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

const bundledScannerWaitBudget = 5 * time.Minute

type staticHeadSHA struct {
	sha string
}

func (s staticHeadSHA) HeadSHA(context.Context, string) (string, error) {
	return s.sha, nil
}

func commitSecurityLanding(t *testing.T, sqlDB db.Handle, projectDir, delegationID string) (*scan.SQLStore, wire.CodeScan) {
	t.Helper()
	jobID := uuid.NewString()
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	workerStore := worker.NewSQLStore(sqlDB)
	testutil.FailErr(t, "InsertTask", workerStore.InsertTask(t.Context(), wire.WorkerTask{
		ID: jobID, Prompt: "security fixture", Brief: "security fixture",
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: projectDir,
		DelegationID: delegationID, AgentType: "implementer", ExecutionTarget: wire.ExecutionTargetLocal,
		Status: wire.WorkerStatusComplete,
	}))
	branch := enginepaths.JobBranchDir(enginepaths.WorkerBranchesRootUnder(testbaseline.DataDir(t, sqlDB)), projectDir, jobID)
	bound, err := workerStore.SetWorkerWorkspace(t.Context(), jobID, branch, testbaseline.Durable(t, sqlDB, jobID, t.TempDir()))
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	if !bound {
		t.Fatal("expected workspace root CAS to win on a fresh job")
	}
	testutil.FailErr(t, "SetMergeStatus", workerStore.SetMergeStatus(t.Context(), jobID, wire.WorkerMergeStatusPending))
	token, claimed, err := workerStore.BeginMergeApply(t.Context(), jobID)
	testutil.FailErr(t, "BeginMergeApply", err)
	if !claimed {
		t.Fatal("expected merge apply claim on a pending overlay")
	}
	plan := obligation.Plan{
		ID: uuid.NewString(), ScanID: uuid.NewString(), AssessmentID: uuid.NewString(), WorkerJobID: jobID, CanonicalPath: projectDir,
		DelegationID: delegationID, ScannerID: "sast-one", ChangedPaths: []string{"main.go"},
		ExecutionManifest: wire.ScanExecutionManifest{
			SchemaVersion: "v1", ScannerID: "sast-one", Engine: "security-test", Driver: "security-test",
			ScopeKind: "source_driver", DefinitionFingerprint: strings.Repeat("a", 64),
			FingerprintScheme: wire.ScanFingerprintScheme,
		},
		ExecutionFingerprint: strings.Repeat("b", 64), FingerprintScheme: wire.ScanFingerprintScheme,
		Required: true, CreatedAt: time.Now().UTC(),
	}
	testutil.FailErr(t, "CommitPromotion", workerStore.CommitPromotion(t.Context(), jobID, token, worker.PromotionCommit{Plan: plan}))
	store := scan.NewSQLStore(sqlDB)
	testutil.FailErr(t, "PublishPending", scantest.Coordinator(t, store, staticHeadSHA{sha: "closeout-head"}).PublishPending(t.Context(), plan.ScanID))
	rec, err := store.Get(t.Context(), plan.ScanID)
	testutil.FailErr(t, "Get scan", err)
	if rec == nil {
		t.Fatal("scan obligation missing")
	}
	return store, *rec
}

func completeSecurityLanding(t *testing.T, store *scan.SQLStore, evidenceStore inspector.EvidenceStore, projectDir string, rec wire.CodeScan) {
	t.Helper()
	result := &scanoutput.Result{}
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	if claimed.ID != rec.ID {
		t.Fatalf("claimed scan %q want %q", claimed.ID, rec.ID)
	}
	won, err := store.MarkComplete(t.Context(), claimed, result)
	testutil.FailErr(t, "MarkComplete", err)
	if !won {
		t.Fatalf("scan %s was not open for completion", rec.ID)
	}
	completed, err := store.Get(t.Context(), rec.ID)
	testutil.FailErr(t, "get completed scan", err)
	ins := inspector.NewSimpleInspector(evidenceStore)
	ins.ProjectDir = func(context.Context, string) (string, error) { return projectDir, nil }
	ing := &scan.IngesterImpl{
		Inspector: ins, Module: scancfg.DefaultModuleConfig(),
		Budget: scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()), BlockOn: []string{"error"},
	}
	evidenceRec, err := ing.Ingest(t.Context(), scan.ScanSourceRegistry, result, scan.IngestMeta{
		ScanID: rec.ID, ProjectDir: projectDir, HeadSHA: rec.HeadSHA,
		SourceSnapshotID: rec.SourceSnapshotID, DelegationID: rec.DelegationID,
		AssessmentID: completed.AssessmentID, CoverageStatus: completed.CoverageStatus,
		ExecutionManifest: completed.ExecutionManifest, ExecutionFingerprint: completed.ExecutionFingerprint,
		TaskID: wire.ScanTaskID(rec.ID), Scanner: securityScannerContract(rec.ScannerID), Categories: rec.Categories,
	})
	testutil.FailErr(t, "Ingest", err)
	testutil.FailErr(t, "SaveIngest", store.SaveIngest(t.Context(), rec.ID, evidenceRec))
}

func securityScannerContract(id string) scancatalog.ScannerContract {
	return scancatalog.ScannerContract{
		ScannerID: id, Engine: "security-test", Driver: "security-test",
		Scope:                 scancatalog.ScopeSourceDriver,
		DefinitionFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

// scanFixtureDir returns the bundled scan fixture.
func scanFixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "testdata", "scan"))
}

func securityToolContext(sessionID, dir, agent string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	return tools.ToolContext{SessionID: sessionID, Roots: roots, ActiveRootID: "r1", Agent: agent}
}

// projectScan is a scan together with the project that addresses it.
type projectScan struct {
	ProjectID string
	wire.CodeScan
}

func scanURL(projectID, scanID string) string {
	return "/v1/projects/" + projectID + "/scans/" + scanID
}

func enqueueCodeScan(t *testing.T, srv *api.Server, projectDir string, categories []wire.ScanCategory) projectScan {
	t.Helper()
	project := createProjectHTTP(t, srv, projectDir)
	return projectScan{ProjectID: project.ID, CodeScan: enqueueSingleScanner(t, srv, project.ID, categories)}
}

func enqueueSingleScanner(t *testing.T, srv *api.Server, projectID string, categories []wire.ScanCategory) wire.CodeScan {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	srv.WaitForBackground(ctx)
	testutil.FailErr(t, "settle project attachment", ctx.Err())
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedRequest(t, http.MethodGet, "/v1/projects/"+projectID+"/security", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET security status = %d body=%s", w.Code, w.Body.String())
	}
	var overview wire.SecurityOverview
	testutil.FailErr(t, "decode security overview", json.Unmarshal(w.Body.Bytes(), &overview))
	var scannerIDs []string
	for _, scanner := range overview.Scanners {
		matches := true
		for _, category := range categories {
			if !slices.Contains(scanner.Categories, category) {
				matches = false
			}
		}
		if matches {
			scannerIDs = append(scannerIDs, scanner.ID)
		}
	}
	if len(scannerIDs) != 1 {
		t.Fatalf("scanner selection for %v = %v, want one scanner", categories, scannerIDs)
	}
	body, err := json.Marshal(wire.FullScanRequest{ScannerIDs: scannerIDs})
	testutil.FailErr(t, "marshal full scan request", err)
	req := authedRequest(t, http.MethodPost, "/v1/projects/"+projectID+"/scans", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /v1/projects/%s/scans status = %d body=%s", projectID, w.Code, w.Body.String())
	}
	var created wire.FullScanResponse
	testutil.FailErr(t, "decode full scan response", json.Unmarshal(w.Body.Bytes(), &created))
	if len(created.Passes) != 1 || len(created.Passes[0].Members) != 1 {
		t.Fatalf("single-scanner response = %+v, want one pass of scanner %s", created, scannerIDs[0])
	}
	pass := created.Passes[0]
	if pass.Members[0].ScannerID != scannerIDs[0] {
		t.Fatalf("single-scanner pass member = %+v, want scanner %s", pass.Members[0], scannerIDs[0])
	}
	return waitFullPassMemberScan(t, srv, projectID, pass)
}

// waitFullPassMemberScan returns the scan the pass's only member starts. A
// cadence dispatch that already holds the scanner's claim defers the member to
// the cadence's next wake, so the accepted response may still show it waiting.
func waitFullPassMemberScan(t *testing.T, srv *api.Server, projectID string, pass wire.SecurityFullPass) wire.CodeScan {
	t.Helper()
	member := pass.Members[0]
	testutil.WaitFor(t, 15*time.Second, func() bool {
		switch member.Phase {
		case wire.FullPassMemberStarted:
			if member.Scan == nil {
				t.Fatalf("pass %s member %s started without a scan", pass.AssessmentID, member.ScannerID)
			}
			return true
		case wire.FullPassMemberNotStarted:
			t.Fatalf("pass %s member %s left the pass before starting", pass.AssessmentID, member.ScannerID)
		case wire.FullPassMemberWaitingForScanner, wire.FullPassMemberWaitingForPass:
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedRequest(t, http.MethodGet, "/v1/projects/"+projectID+"/security", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET security status = %d body=%s", w.Code, w.Body.String())
		}
		var overview wire.SecurityOverview
		testutil.FailErr(t, "decode security overview", json.Unmarshal(w.Body.Bytes(), &overview))
		for _, candidate := range []*wire.SecurityFullPass{overview.Running, overview.LastFull} {
			if candidate != nil && candidate.AssessmentID == pass.AssessmentID && len(candidate.Members) == 1 {
				member = candidate.Members[0]
			}
		}
		return false
	})
	return *member.Scan
}

func waitScanComplete(t *testing.T, srv *api.Server, projectID, scanID string, timeout time.Duration) wire.CodeScan {
	t.Helper()
	var got wire.CodeScan
	if !testutil.WaitForNoFatal(timeout, func() bool {
		getReq := authedRequest(t, http.MethodGet, scanURL(projectID, scanID)+"?view=full", nil)
		getW := httptest.NewRecorder()
		srv.ServeHTTP(getW, getReq)
		if getW.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", scanURL(projectID, scanID), getW.Code)
		}
		if err := json.Unmarshal(getW.Body.Bytes(), &got); err != nil {
			testutil.FailErr(t, "unmarshal scan get", err)
		}
		switch got.Status {
		case wire.CodeScanStatusComplete, wire.CodeScanStatusFailed, wire.CodeScanStatusTimedOut,
			wire.CodeScanStatusCanceled, wire.CodeScanStatusSuperseded:
			return true
		default:
			return false
		}
	}) {
		t.Fatalf("scan %s did not finish within %s: scanner=%s status=%s error=%s",
			scanID, timeout, got.ScannerID, got.Status, got.Error)
	}
	return got
}

func waitScanPackComplete(t *testing.T, h *wiring.Harness, projectDir string, categories []any, timeout time.Duration) scantoolapi.ScanPackToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	args := map[string]any{"completion": "summary", "timeout_ms": timeout.Milliseconds()}
	if len(categories) > 0 {
		args["categories"] = categories
	}
	raw, err := h.ToolRegistry.Run(ctx, "scan_pack", args, securityToolContext("", projectDir, "coordinator"))
	testutil.FailErr(t, "wait for scan_pack completion", err)
	var result scantoolapi.ScanPackToolResult
	testutil.FailErr(t, "decode scan_pack summary", json.Unmarshal([]byte(raw), &result))
	switch result.Status {
	case wire.CodeScanStatusComplete, wire.CodeScanStatusFailed, wire.CodeScanStatusTimedOut,
		wire.CodeScanStatusCanceled, wire.CodeScanStatusSuperseded:
		return result
	default:
		t.Fatalf("scan pack did not finish within %s: status=%s error=%s scans=%+v", timeout, result.Status, result.Error, result.PerScan)
		return result
	}
}

func getCodeScan(t *testing.T, srv *api.Server, projectID, scanID string) wire.CodeScan {
	t.Helper()
	getReq := authedRequest(t, http.MethodGet, scanURL(projectID, scanID)+"?view=full", nil)
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", scanURL(projectID, scanID), getW.Code, getW.Body.String())
	}
	var got wire.CodeScan
	if err := json.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		testutil.FailErr(t, "unmarshal scan get", err)
	}
	return got
}

func assertScanPackPerEngine(t *testing.T, srv *api.Server, projectID string, payload scantoolapi.ScanPackToolResult, wantEngines map[string]engineScanExpect) {
	t.Helper()
	if len(payload.ScanIDs) != len(wantEngines) {
		t.Fatalf("scan_ids count = %d, want %d (%v)", len(payload.ScanIDs), len(wantEngines), payload.ScanIDs)
	}
	seen := map[string]wire.CodeScan{}
	for _, id := range payload.ScanIDs {
		rec := getCodeScan(t, srv, projectID, id)
		if rec.ScannerID == "" {
			t.Fatalf("scan %s missing scanner_id", id)
		}
		seen[rec.ScannerID] = rec
	}
	for scannerID, expect := range wantEngines {
		rec, ok := seen[scannerID]
		if !ok {
			t.Fatalf("missing scan row for engine %q (scan_ids=%v)", scannerID, payload.ScanIDs)
		}
		assertScanFindings(t, rec, expect.wantFile, expect.minFindings)
	}
}

type engineScanExpect struct {
	minFindings int
	wantFile    string
}

func assertScanFindings(t *testing.T, got wire.CodeScan, wantFile string, minCount int) {
	t.Helper()
	if got.Status == wire.CodeScanStatusFailed {
		t.Fatalf("scan failed: %s", got.Error)
	}
	if got.Status != wire.CodeScanStatusComplete {
		t.Fatalf("scan status = %q", got.Status)
	}
	if got.FindingsCount < minCount {
		t.Fatalf("findings_count = %d, want >= %d", got.FindingsCount, minCount)
	}
	if wantFile == "" {
		return
	}
	for _, f := range got.Findings {
		for _, loc := range f.Locations {
			if strings.Contains(loc.URI, wantFile) {
				return
			}
		}
	}
	if len(got.Findings) == 0 {
		// The finding count includes audit rows omitted by the view cap.
		return
	}
	t.Fatalf("no finding with file containing %q (findings_count=%d, audit_rows=%d)", wantFile, got.FindingsCount, len(got.Findings))
}
