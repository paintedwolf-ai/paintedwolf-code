package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/report/reporttest"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGetWorkflowRunReport_Integration(t *testing.T) {
	h := newReportTestHarness(t)
	run := h.seedSecuritySurveyRun(t, "run_report_full_001")
	h.seedRunReport(t, run)
	h.seedVerdict(t, run)
	h.seedFindings(t, run)

	input, ok, err := h.srv.Workflow.BuildRunReportInput(t.Context(), run.ID)
	testutil.FailErr(t, "buildRunReportInput", err)
	if !ok {
		t.Fatal("want report available")
	}
	reporttest.AssertReportInput(t, input)
	if len(input.Verdicts) == 0 || len(input.Findings) == 0 || len(input.Evidence) == 0 {
		t.Fatalf("want verdicts, findings and evidence assembled; input=%+v", input)
	}
	if input.HeadSHA != "a1b2c3d" {
		t.Fatalf("HeadSHA = %q", input.HeadSHA)
	}

	// Both review phases recorded a verdict. A projection that kept only one
	// would drop the claims the survey exists to state.
	if len(input.Verdicts) != 2 {
		t.Fatalf("verdicts = %+v, want one per review phase in manifest order", input.Verdicts)
	}
	claims, challenge := input.Verdicts[0], input.Verdicts[1]
	if claims.Phase != "claims" || len(claims.Claims) != 1 || claims.Decision != "CLAIMED" {
		t.Fatalf("claims verdict = %+v, want the schema's claims-typed member projected as claims", claims)
	}
	if challenge.Phase != "challenge" || challenge.Decision != "CHALLENGED" {
		t.Fatalf("challenge verdict = %+v, want the terminal decision last", challenge)
	}
	if len(challenge.Claims) != 1 || challenge.Claims[0].Status != "survives" {
		t.Fatalf("challenge claims = %+v, want the per-claim outcome", challenge.Claims)
	}
	// The reserved citation channels travel beside the verdict, never as fields.
	for _, v := range input.Verdicts {
		for _, f := range v.Fields {
			if strings.Contains(f.Value, "map[") || f.Name == "cited_evidence" || f.Name == "cited_urls" {
				t.Fatalf("verdict %q field %q = %q; reserved channels are not fields", v.Phase, f.Name, f.Value)
			}
		}
	}
	// Each phase's citations reach the record they name.
	var citedByChallenge bool
	for _, e := range input.Evidence {
		for _, by := range e.CitedBy {
			if by == "challenge" {
				citedByChallenge = true
			}
		}
	}
	if !citedByChallenge {
		t.Fatalf("evidence = %+v, want the challenge phase's own citation attributed", input.Evidence)
	}

	if input.Scan == nil || input.Scan.Total != 46 || input.Scan.Listed != 1 || input.Scan.Merged != 5 {
		t.Fatalf("scan = %+v, want the reported total alongside the listed and merged rows", input.Scan)
	}
	// Findings come from the closeout; claims carry the review's class.
	if len(input.Findings) != 1 || input.Findings[0].Disposition != "act" {
		t.Fatalf("findings = %+v, want the closeout's one finding with its disposition", input.Findings)
	}
	if len(input.Claims) != 1 || input.Claims[0].Class != "held" || input.Claims[0].Title != "User input reaches a query" {
		t.Fatalf("claims = %+v, want the survived claim held", input.Claims)
	}
	if input.Ask == nil || input.Brief == nil || len(input.Brief.Rated) != 1 {
		t.Fatalf("ask = %+v brief = %+v, want the ask and the finding rated", input.Ask, input.Brief)
	}
	if input.Inventory == nil || input.Inventory.Unaccounted != 1 {
		t.Fatalf("inventory = %+v, want the unlinked scanner group counted", input.Inventory)
	}
	if got := input.Scan.ByLevel["info"]; got != 45 {
		t.Fatalf("scan.by_level[info] = %d, want the levels the scan graded", got)
	}
	if len(input.ScanRules) != 1 || input.ScanRules[0].ID != "go.sql.injection" {
		t.Fatalf("scan rules = %+v, want one entry per rule the listed rows reference", input.ScanRules)
	}
	if input.Headline == "" || len(input.Limits) == 0 {
		t.Fatalf("want the closeout's headline and limits carried; headline=%q limits=%v", input.Headline, input.Limits)
	}
	if input.Workflow == nil || input.Workflow.ID != "security-survey" {
		t.Fatalf("workflow = %+v, want the run's workflow named for the colophon", input.Workflow)
	}
	if input.StartedAt == "" {
		t.Fatal("want started_at, so the cover can state how long the run took")
	}

	first := h.getReport(t, run.ID)
	assertPDFOK(t, first)
	second := h.getReport(t, run.ID)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d", second.Code)
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("two GETs on completed run must produce identical PDF bytes")
	}
}

func TestGetWorkflowRunReport_NotAvailable(t *testing.T) {
	h := newReportTestHarness(t)
	now := time.Now().UTC()

	for _, workflowID := range []string{"plan", "implement"} {
		run := h.createCompletedRun(t, "run_disabled_"+workflowID, workflowID, "done", now)
		h.seedMinimalRunReport(t, run)
		rec := h.getReport(t, run.ID)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("disabled %s report status = %d body = %s", workflowID, rec.Code, rec.Body.String())
		}
		assertErrorCode(t, rec, "report_not_found")
	}

	survey := h.createCompletedRun(t, "run_no_report", "security-survey", "done", now)
	rec := h.getReport(t, survey.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no report status = %d body = %s", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "report_not_found")

	running := h.createCompletedRun(t, "run_still_running", "security-survey", "done", now)
	running.Status = wire.WorkflowRunStatusRunning
	running.CompletedAt = nil
	testutil.FailErr(t, "update running run", h.runStore.Update(t.Context(), running))
	h.seedRunReport(t, running)
	rec = h.getReport(t, running.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("nonterminal status = %d body = %s", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "report_not_found")
}

type reportTestHarness struct {
	srv       *Server
	deps      Dependencies
	store     *store.SQL
	runStore  *workflow.SQLStore
	wfMgr     *workflow.RunManager
	scanStore *scan.SQLStore
	project   *project.Project
	sqlDB     db.Handle
	hostDir   string
	workDir   string
}

func newReportTestHarness(t *testing.T, opts ...testDeps) *reportTestHarness {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	sqlDB := testdbfixture.Open(t, "report.db")

	store := store.NewSQL(sqlDB)
	wfReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	runStore := workflow.NewSQLStore(sqlDB)
	wfMgr := workflow.NewManager(runStore, store, wfReg, nil)
	wfMgr.Resolver = workflow.ManifestResolver{}
	hostDir := t.TempDir()
	wfMgr.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	wfMgr.EvidenceProjectDir = func(context.Context, string) (string, error) { return hostDir, nil }

	scanStore := scan.NewSQLStore(sqlDB)
	workDir := t.TempDir()
	projReg := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projReg, workDir)
	testutil.FailErr(t, "CreateWithRoot", err)
	name := "acme/app"
	_, err = projReg.Patch(t.Context(), p.ID, project.PatchParams{Name: &name})
	testutil.FailErr(t, "Patch project name", err)

	deps := Dependencies{
		Store: store, Projects: projReg,
		Workflows: wfMgr, WorkflowCatalog: workflow.ManifestResolver{}, WorkflowRuns: runStore,
		ScanCoordinator: scantest.Coordinator(t, scanStore, nil), ModuleRoot: root,
	}
	for _, opt := range opts {
		opt(&deps)
	}
	deps = requiredTestDeps(t, deps)

	return &reportTestHarness{
		sqlDB: sqlDB,
		srv:   NewServer(deps, nil, TestAPIToken), deps: deps, store: store, runStore: runStore, wfMgr: wfMgr,
		scanStore: scanStore, project: p, hostDir: hostDir, workDir: workDir,
	}
}

// restartWithVisualStore rebuilds the server over the same stores, serving
// artifacts from visuals.
func (h *reportTestHarness) restartWithVisualStore(visuals visual.Store) {
	h.deps.VisualStore = visuals
	h.srv = NewServer(h.deps, nil, TestAPIToken)
}

func (h *reportTestHarness) seedSecuritySurveyRun(t *testing.T, runID string) *wire.WorkflowRun {
	t.Helper()
	completed := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	return h.createCompletedRun(t, runID, "security-survey", "done", completed)
}

func (h *reportTestHarness) createCompletedRun(t *testing.T, runID, workflowID, phase string, at time.Time) *wire.WorkflowRun {
	t.Helper()
	sess, err := h.store.Create(t.Context(), wire.CreateSessionRequest{
		ProjectID: h.project.ID,
		Posture:   wire.SessionPostureVet,
	}, h.project.ID)
	testutil.FailErr(t, "session create", err)
	run := &wire.WorkflowRun{
		ID: runID, SessionID: sess.ID, ProjectID: h.project.ID,
		WorkflowID: workflowID, WorkflowVersion: "1.0.0",
		Status: wire.WorkflowRunStatusComplete, CurrentPhase: phase,
		CompletedAt: &at, CreatedAt: at, UpdatedAt: at,
	}
	testutil.FailErr(t, "create run", h.runStore.CreateState(t.Context(), run, h.workDir, nil))
	return run
}

func (h *reportTestHarness) seedRunReport(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	h.markReportDelivered(t, run)
	testutil.FailErr(t, "append run report", h.store.AppendMessages(t.Context(), run.SessionID, wire.Message{
		ID: "msg_run_report", Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport,
		CompletionReport: &wire.CompletionReportMeta{
			Scope: wire.CompletionReportScopeRun, SurfaceID: "implement_synthesis", Phase: "report",
			Headline: "One SQL injection survives adjudication; the loopback bypass is hardening only.",
			Limits:   []string{"Static review only; nothing was executed."},
			Findings: []wire.CompletionReportFinding{{
				ID: "app-1", Title: "The sort column reaches the query builder", Severity: "high",
				Disposition: wire.CompletionReportFindingDispositionAct,
			}},
			Ask: &wire.CompletionReportAsk{Do: "Hold exposure until the query fix lands.", Effort: wire.CompletionReportAskEffortSmall},
		},
		Content:    "## Summary\n\nOne in-scope vulnerability survives adjudication.",
		Visibility: wire.MessageVisibilityTranscript, WorkflowRunID: run.ID,
		Grounding: &wire.CitationGrounding{
			Traced: true, CitedURLs: []string{"https://owasp.org/www-community/attacks/SQL_Injection"},
			EvidenceRecordCount: 12,
			EvidenceRecords: []wire.CitationGroundingEvidenceRecord{{
				Handle: "grep#1", Kind: "grep", Path: "internal/auth/auth.go", Line: 88,
				Excerpt: `db.Query("… " + user)`, Fidelity: "observed",
				URLTitles: map[string]string{
					"https://owasp.org/www-community/attacks/SQL_Injection": "SQL injection",
				},
			}},
		},
	}))
}

func TestGetWorkflowRunReport_ArtifactsFromOverlay(t *testing.T) {
	h := newReportTestHarness(t)
	dataDir := t.TempDir()
	lookup := func(_ context.Context, _ string) (string, error) { return h.project.ID, nil }
	artifactsDir := func(pid string) (string, error) {
		dir := filepath.Join(dataDir, "projects", pid, "artifacts")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}
	hot := visual.NewDurableStore(visual.DurableConfig{
		ArtifactsDir: artifactsDir,
		Lookup:       lookup,
		Records:      visual.NewRecords(h.sqlDB, eventoutbox.New(h.sqlDB, nil), visual.ArtifactProjection{}),
	})
	h.restartWithVisualStore(hot)

	run := h.seedSecuritySurveyRun(t, "run_report_arts_001")
	h.seedRunReport(t, run)
	h.seedVerdict(t, run)
	h.seedFindings(t, run)

	png := visual.TestPNG1x1Bytes()
	renderArt, err := hot.Put(t.Context(), run.SessionID, visual.Entry{
		Meta: wire.VisualArtifact{
			Mime: "image/png", Source: wire.VisualArtifactSourceRender,
			Caption: "Signup — empty state",
		},
		Bytes: png,
	})
	testutil.FailErr(t, "put render", err)
	captureArt, err := hot.Put(t.Context(), run.SessionID, visual.Entry{
		Meta: wire.VisualArtifact{
			Mime: "image/png", Source: wire.VisualArtifactSourceCapture,
			Caption: "Login form after submit", EvidenceHandle: "capture#1",
		},
		Bytes: png,
	})
	testutil.FailErr(t, "put capture", err)
	h.seedToolVisual(t, run, renderArt)
	h.seedToolVisual(t, run, captureArt)

	// Restart: new DurableStore over the same overlay (empty hot tier).
	cold := visual.NewDurableStore(visual.DurableConfig{
		ArtifactsDir: artifactsDir,
		Lookup:       lookup,
		Records:      visual.NewRecords(h.sqlDB, eventoutbox.New(h.sqlDB, nil), visual.ArtifactProjection{}),
	})
	h.restartWithVisualStore(cold)

	input, ok, err := h.srv.Workflow.BuildRunReportInput(t.Context(), run.ID)
	testutil.FailErr(t, "buildRunReportInput", err)
	if !ok {
		t.Fatal("want report available")
	}
	if len(input.Artifacts) != 2 {
		t.Fatalf("artifacts = %d want 2 after overlay resolve", len(input.Artifacts))
	}
	var sawRender bool
	for _, a := range input.Artifacts {
		if a.EvidenceHandle == "" {
			sawRender = true
		}
	}
	if !sawRender {
		t.Fatal("want a render artifact, which is what the Visuals section carries")
	}
	var sawCapture bool
	for _, a := range input.Artifacts {
		if a.EvidenceHandle != "" {
			sawCapture = true
		}
		if len(a.Bytes) == 0 {
			t.Fatalf("artifact %s: bytes empty after overlay Get", a.ID)
		}
	}
	if !sawCapture {
		t.Fatal("want capture with evidence_handle")
	}

	rec := h.getReport(t, run.ID)
	assertPDFOK(t, rec)
}

func TestGetWorkflowRunReport_MissingArtifactBytesShrinks(t *testing.T) {
	h := newReportTestHarness(t, func(d *Dependencies) { d.VisualStore = visual.NewMemoryStore() })
	run := h.seedSecuritySurveyRun(t, "run_report_missing_art")
	h.seedRunReport(t, run)
	h.seedToolVisual(t, run, wire.VisualArtifact{
		ID: "art_unavailable", Mime: "image/png", Source: wire.VisualArtifactSourceRender,
		Caption: "gone", StoreRef: true,
	})

	input, ok, err := h.srv.Workflow.BuildRunReportInput(t.Context(), run.ID)
	testutil.FailErr(t, "buildRunReportInput", err)
	if !ok {
		t.Fatal("want report available")
	}
	if len(input.Artifacts) != 0 {
		t.Fatalf("missing artifact must be omitted; got %+v", input.Artifacts)
	}
	rec := h.getReport(t, run.ID)
	assertPDFOK(t, rec)
}

func (h *reportTestHarness) seedToolVisual(t *testing.T, run *wire.WorkflowRun, art wire.VisualArtifact) {
	t.Helper()
	id := "msg_visual_" + art.ID
	vis := art
	vis.Bytes = nil
	vis.StoreRef = true
	testutil.FailErr(t, "append visual tool result", h.store.AppendMessages(t.Context(), run.SessionID, wire.Message{
		ID: id, Role: wire.MessageRoleTool,
		Content: "visual", Visibility: wire.MessageVisibilityTranscript,
		WorkflowRunID: run.ID,
		ToolResult: &wire.ToolResult{
			Content: "ok", Tool: "render_view", Outcome: wire.ToolResultOutcomeCompleted,
			Visual: &vis,
		},
	}))
}

func (h *reportTestHarness) seedMinimalRunReport(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	h.markReportDelivered(t, run)
	testutil.FailErr(t, "append grounded", h.store.AppendMessages(t.Context(), run.SessionID, wire.Message{
		ID: "msg_impl_" + run.ID, Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, Content: "done",
		CompletionReport: &wire.CompletionReportMeta{Scope: wire.CompletionReportScopeRun, Phase: "done"},
		Visibility:       wire.MessageVisibilityTranscript, WorkflowRunID: run.ID,
		Grounding: &wire.CitationGrounding{Traced: true},
	}))
}

// seedVerdict stamps both review phases, as a completed survey does: the
// phase that named the claims, then the phase that challenged them.
func (h *reportTestHarness) seedVerdict(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	at := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	claims := evidence.GateRecord(
		evidence.GateTypeSurveyClaims, "claims", run.ID,
		evidence.GateVerdictPassed, "CLAIMED",
		map[string]any{
			"verdict":      "CLAIMED",
			"threat_model": "HTTP service; unauthenticated clients",
			"claims": `[{"id":"app-1","title":"User input reaches a query","status":"claimed","statement":"user input is concatenated into a query",` +
				`"cited_evidence":[{"path":"internal/auth/auth.go","line":88}]}]`,
			"cited_evidence": []any{
				map[string]any{"handle": "grep#1", "path": "internal/auth/auth.go", "line": 88},
			},
			"cited_urls": []any{},
		},
		"", "", "", 1, at,
	)
	testutil.FailErr(t, "append claims verdict", h.wfMgr.EvidenceStore.Append(t.Context(), h.hostDir, claims))

	challenge := evidence.GateRecord(
		evidence.GateTypeSurveyChallenged, "challenge", run.ID,
		evidence.GateVerdictApproved, "CHALLENGED",
		map[string]any{
			"verdict": "CHALLENGED",
			"challenges": `[{"id":"app-1","status":"survives","statement":"the sort column reaches the builder unvalidated",` +
				`"cited_evidence":[{"path":"internal/auth/auth.go","line":88}]}]`,
			"cited_evidence": []any{
				map[string]any{"handle": "grep#1", "path": "internal/auth/auth.go", "line": 88, "excerpt": `db.Query("… " + user)`},
			},
			"cited_urls": []any{"https://owasp.org/www-community/attacks/SQL_Injection"},
		},
		"", "", "", 1, at.Add(4*time.Minute),
	)
	testutil.FailErr(t, "append challenge verdict", h.wfMgr.EvidenceStore.Append(t.Context(), h.hostDir, challenge))
}

func (h *reportTestHarness) seedFindings(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	at := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	testutil.FailErr(t, "insert scan", h.scanStore.Insert(t.Context(), wire.CodeScan{
		ID: "scan_report_1", CanonicalPath: h.workDir, ScannerID: "semgrep",
		Status: wire.CodeScanStatusComplete, Categories: []wire.ScanCategory{wire.ScanCategorySecurity},
		WorkflowRunID: run.ID, HeadSHA: "a1b2c3d", CompletedAt: &at,
	}, nil, ""))
	testutil.FailErr(t, "save ingest", h.scanStore.SaveIngest(t.Context(), "scan_report_1", evidence.Record{
		Artifacts: map[string]any{
			"findings": []wire.SecurityFinding{{
				RuleID: "go.sql.injection", Level: wire.FindingLevelHigh,
				Message:   "user input concatenated into query",
				Locations: []wire.SecurityFindingLocation{{URI: "internal/auth/auth.go", StartLine: 88}},
			}},
			// A budgeted scan: it reported far more than it stored, and the
			// report has to carry that gap rather than list one row as if it
			// were the whole result.
			"findings_count":    46,
			"findings_stored":   1,
			"findings_merged":   5,
			"findings_by_level": map[string]int{"high": 1, "info": 45},
		},
	}))
}

func (h *reportTestHarness) getReport(t *testing.T, runID string) *httptest.ResponseRecorder {
	t.Helper()
	req := newAuthedRequest(http.MethodGet, "/v1/workflow-runs/"+runID+"/report", nil)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

func assertPDFOK(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, `filename="painted-wolf-code-`) || !strings.Contains(cd, ".pdf") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if rec.Body.Len() < 100 || !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF")) {
		t.Fatalf("pdf body invalid len=%d", rec.Body.Len())
	}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	var errResp wire.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error: %v body=%s", err, rec.Body.String())
	}
	if string(errResp.Code) != code {
		t.Fatalf("code = %q want %q body=%s", errResp.Code, code, rec.Body.String())
	}
}

func (h *reportTestHarness) markReportDelivered(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	_, err := h.wfMgr.StampRunVars(t.Context(), run.ID, func(_ context.Context, _ *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		return workflow.SetGateSatisfied(vars, "topology_report_delivered", true), true, nil
	})
	testutil.FailErr(t, "stamp report delivery", err)
}

// A review paused as review_blocked serves its retained snapshot as an
// incomplete report; a run paused for any other reason has no report yet.
func TestBlockedReviewServesItsRetainedSnapshot(t *testing.T) {
	h := newReportTestHarness(t)
	run := h.seedSecuritySurveyRun(t, "run_review_blocked")
	repair := workflow.ReviewRepair{
		ID: "repair", Phase: "claims", State: "blocked", UpdatedAt: time.Now().UTC(),
		Responses: []workflow.ReviewRepairResponse{{ID: "response"}},
		Snapshot:  &workflow.ReviewSnapshot{Vars: map[string]any{}, Unavailable: []string{"scan ledger"}},
	}
	_, err := h.wfMgr.StampRunVars(t.Context(), run.ID, func(_ context.Context, _ *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		vars["review_repairs"] = []workflow.ReviewRepair{repair}
		return vars, true, nil
	})
	testutil.FailErr(t, "stamp blocked repair", err)
	pause := func(reason string) {
		t.Helper()
		current, err := h.runStore.Get(t.Context(), run.ID)
		testutil.FailErr(t, "get run", err)
		current.Status, current.PauseReason, current.CurrentPhase, current.CompletedAt = wire.WorkflowRunStatusPaused, reason, "claims", nil
		testutil.FailErr(t, "pause run", h.runStore.Update(t.Context(), current))
	}

	pause(workflow.ReviewBlockedReason)
	input, ok, err := h.srv.Workflow.BuildRunReportInput(t.Context(), run.ID)
	testutil.FailErr(t, "build blocked report", err)
	if !ok || input.Kind != report.BlockedReviewSnapshot || input.Completeness() != report.CompletenessIncomplete {
		t.Fatalf("blocked review report = ok %v kind %q", ok, input.Kind)
	}
	if !strings.Contains(input.Synthesis, "Unavailable when paused: scan ledger.") {
		t.Fatalf("snapshot omitted its unavailable sources: %s", input.Synthesis)
	}
	assertPDFOK(t, h.getReport(t, run.ID))

	pause("user")
	if _, ok, err := h.srv.Workflow.BuildRunReportInput(t.Context(), run.ID); err != nil || ok {
		t.Fatalf("an ordinarily paused run offered a report: ok %v err %v", ok, err)
	}
}

func TestReportDownloadRequiresRecordedDelivery(t *testing.T) {
	h := newReportTestHarness(t)
	run := h.seedSecuritySurveyRun(t, "run_no_delivery")
	h.seedRunReport(t, run)
	_, err := h.wfMgr.StampRunVars(t.Context(), run.ID, func(_ context.Context, _ *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		return workflow.SetGateSatisfied(vars, "topology_report_delivered", false), true, nil
	})
	testutil.FailErr(t, "clear delivery", err)
	rec := h.getReport(t, run.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("undelivered report status = %d", rec.Code)
	}
	assertErrorCode(t, rec, "report_not_found")
}
