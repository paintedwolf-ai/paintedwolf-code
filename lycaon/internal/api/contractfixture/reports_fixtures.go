package contractfixture

import (
	"bytes"
	"context"
	"encoding/json"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func AssertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	var errResp wire.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error: %v body=%s", err, rec.Body.String())
	}
	if string(errResp.Code) != code {
		t.Fatalf("code = %q want %q body=%s", errResp.Code, code, rec.Body.String())
	}
}

func AssertPDFOK(t *testing.T, rec *httptest.ResponseRecorder) {
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

func NewReportTestHarness(t *testing.T, opts ...TestDeps) *ReportTestHarness {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	root := configlayout.FindModuleRoot()

	sqlDB := testdbfixture.Open(t, "report.db")

	store := store.NewSQL(sqlDB)
	wfReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	runStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(runStore, store, wfReg, nil)

	hostDir := t.TempDir()
	wfMgr.Verdicts.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	wfMgr.SetEvidenceProjectDir(func(context.Context, string) (string, error) { return hostDir, nil })

	scanStore := scan.NewSQLStore(sqlDB)
	workDir := t.TempDir()
	projReg := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projReg, workDir)
	testutil.FailErr(t, "CreateWithRoot", err)
	name := "acme/app"
	_, err = projReg.Patch(t.Context(), p.ID, project.PatchParams{Name: &name})
	testutil.FailErr(t, "Patch project name", err)

	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: projReg}, Workflow: hostapi.WorkflowDependencies{
		Workflows: wfMgr, WorkflowCatalog: workflowcatalog.Resolver{}, WorkflowRuns: runStore}, Scans: hostapi.ScansDependencies{
		ScanCoordinator: scantest.Coordinator(t, scanStore, nil)}, Storage: hostapi.StorageDependencies{ModuleRoot: root}}
	for _, opt := range opts {
		opt(&deps)
	}
	deps = RequiredTestDeps(t, deps)

	return &ReportTestHarness{
		SqlDB: sqlDB,
		Srv:   hostapi.NewServer(deps, nil, hostapi.TestAPIToken), Deps: deps, Store: store, RunStore: runStore, WfMgr: wfMgr,
		ScanStore: scanStore, Project: p, HostDir: hostDir, WorkDir: workDir,
	}
}

// restartWithVisualStore rebuilds the server over the same stores, serving
// artifacts from visuals.

type ReportTestHarness struct {
	Srv       *hostapi.Server
	Deps      hostapi.Dependencies
	Store     *store.SQL
	RunStore  *runstate.Repository
	WfMgr     *workflow.RunManager
	ScanStore *scan.SQLStore
	Project   *project.Project
	SqlDB     db.Handle
	HostDir   string
	WorkDir   string
}

func (h *ReportTestHarness) SeedSessionReport(t *testing.T, sessionID, msgID string) {
	t.Helper()
	testutil.FailErr(t, "append session report", h.Store.AppendMessages(t.Context(), sessionID, wire.Message{
		ID: msgID, Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport,
		Content: "## What changed\n\nThe bucket refills from a monotonic clock (" + msgID + ").",
		CompletionReport: &wire.CompletionReportMeta{
			Scope: wire.CompletionReportScopeSession, SurfaceID: "implement_synthesis",
		},
		Visibility: wire.MessageVisibilityTranscript,
		Grounding: &wire.CitationGrounding{
			Traced: true,
			EvidenceRecords: []wire.CitationGroundingEvidenceRecord{{
				Handle: "read#1", Kind: "read", Path: "internal/limiter/bucket.go", Line: 42,
				Excerpt: "now := time.Now()", Fidelity: "observed",
			}},
		},
	}))
}

func (h *ReportTestHarness) RestartWithVisualStore(visuals visual.Store) {
	h.Deps.Source.VisualStore = visuals
	h.Srv = hostapi.NewServer(h.Deps, nil, hostapi.TestAPIToken)
}

func (h *ReportTestHarness) SeedSecuritySurveyRun(t *testing.T, runID string) *wire.WorkflowRun {
	t.Helper()
	completed := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	return h.CreateCompletedRun(t, runID, "security-survey", "done", completed)
}

func (h *ReportTestHarness) CreateCompletedRun(t *testing.T, runID, workflowID, phase string, at time.Time) *wire.WorkflowRun {
	t.Helper()
	sess, err := h.Store.Create(t.Context(), wire.CreateSessionRequest{
		ProjectID: h.Project.ID,
		Posture:   wire.SessionPostureVet,
	}, h.Project.ID)
	testutil.FailErr(t, "session create", err)
	run := &wire.WorkflowRun{
		ID: runID, SessionID: sess.ID, ProjectID: h.Project.ID,
		WorkflowID: workflowID, WorkflowVersion: "1.0.0",
		Status: wire.WorkflowRunStatusComplete, CurrentPhase: phase,
		CompletedAt: &at, CreatedAt: at, UpdatedAt: at,
	}
	testutil.FailErr(t, "create run", h.RunStore.State.CreateState(t.Context(), run, h.WorkDir, nil))
	return run
}

func (h *ReportTestHarness) SeedRunReport(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	h.MarkReportDelivered(t, run)
	testutil.FailErr(t, "append run report", h.Store.AppendMessages(t.Context(), run.SessionID, wire.Message{
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

func (h *ReportTestHarness) SeedToolVisual(t *testing.T, run *wire.WorkflowRun, art wire.VisualArtifact) {
	t.Helper()
	id := "msg_visual_" + art.ID
	vis := art
	vis.Bytes = nil
	vis.StoreRef = true
	testutil.FailErr(t, "append visual tool result", h.Store.AppendMessages(t.Context(), run.SessionID, wire.Message{
		ID: id, Role: wire.MessageRoleTool,
		Content: "visual", Visibility: wire.MessageVisibilityTranscript,
		WorkflowRunID: run.ID,
		ToolResult: &wire.ToolResult{
			Content: "ok", Tool: "render_view", Outcome: wire.ToolResultOutcomeCompleted,
			Visual: &vis,
		},
	}))
}

func (h *ReportTestHarness) SeedMinimalRunReport(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	h.MarkReportDelivered(t, run)
	testutil.FailErr(t, "append grounded", h.Store.AppendMessages(t.Context(), run.SessionID, wire.Message{
		ID: "msg_impl_" + run.ID, Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, Content: "done",
		CompletionReport: &wire.CompletionReportMeta{Scope: wire.CompletionReportScopeRun, Phase: "done"},
		Visibility:       wire.MessageVisibilityTranscript, WorkflowRunID: run.ID,
		Grounding: &wire.CitationGrounding{Traced: true},
	}))
}

// seedVerdict stamps both review phases, as a completed survey does: the
// phase that named the claims, then the phase that challenged them.

func (h *ReportTestHarness) SeedVerdict(t *testing.T, run *wire.WorkflowRun) {
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
	testutil.FailErr(t, "append claims verdict", h.WfMgr.Verdicts.EvidenceStore.Append(t.Context(), h.HostDir, claims))

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
	testutil.FailErr(t, "append challenge verdict", h.WfMgr.Verdicts.EvidenceStore.Append(t.Context(), h.HostDir, challenge))
}

func (h *ReportTestHarness) SeedFindings(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	at := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	testutil.FailErr(t, "insert scan", h.ScanStore.Insert(t.Context(), wire.CodeScan{
		ID: "scan_report_1", CanonicalPath: h.WorkDir, ScannerID: "semgrep",
		Status: wire.CodeScanStatusComplete, Categories: []wire.ScanCategory{wire.ScanCategorySecurity},
		WorkflowRunID: run.ID, HeadSHA: "a1b2c3d", CompletedAt: &at,
	}, nil, ""))
	testutil.FailErr(t, "save ingest", h.ScanStore.SaveIngest(t.Context(), "scan_report_1", evidence.Record{
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

func (h *ReportTestHarness) GetReport(t *testing.T, runID string) *httptest.ResponseRecorder {
	t.Helper()
	req := NewAuthedRequest(http.MethodGet, "/v1/workflow-runs/"+runID+"/report", nil)
	rec := httptest.NewRecorder()
	h.Srv.ServeHTTP(rec, req)
	return rec
}

func (h *ReportTestHarness) MarkReportDelivered(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	_, err := h.WfMgr.Phases.Vars.Stamp(t.Context(), run.ID, func(_ context.Context, _ *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		return runstate.SetGateSatisfied(vars, "topology_report_delivered", true), true, nil
	})
	testutil.FailErr(t, "stamp report delivery", err)
}
