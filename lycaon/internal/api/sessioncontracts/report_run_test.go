package sessioncontracts

import (
 workflowrunstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/report/reporttest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGetWorkflowRunReport_Integration(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t)
	run := h.SeedSecuritySurveyRun(t, "run_report_full_001")
	h.SeedRunReport(t, run)
	h.SeedVerdict(t, run)
	h.SeedFindings(t, run)

	input, ok, err := h.Srv.Admin.Workflow.Reports.BuildRunReportInput(t.Context(), run.ID)
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

	first := h.GetReport(t, run.ID)
	contractfixture.AssertPDFOK(t, first)
	second := h.GetReport(t, run.ID)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d", second.Code)
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("two GETs on completed run must produce identical PDF bytes")
	}
}

func TestGetWorkflowRunReport_NotAvailable(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t)
	now := time.Now().UTC()

	for _, workflowID := range []string{"plan", "implement"} {
		run := h.CreateCompletedRun(t, "run_disabled_"+workflowID, workflowID, "done", now)
		h.SeedMinimalRunReport(t, run)
		rec := h.GetReport(t, run.ID)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("disabled %s report status = %d body = %s", workflowID, rec.Code, rec.Body.String())
		}
		contractfixture.AssertErrorCode(t, rec, "report_not_found")
	}

	survey := h.CreateCompletedRun(t, "run_no_report", "security-survey", "done", now)
	rec := h.GetReport(t, survey.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no report status = %d body = %s", rec.Code, rec.Body.String())
	}
	contractfixture.AssertErrorCode(t, rec, "report_not_found")

	running := h.CreateCompletedRun(t, "run_still_running", "security-survey", "done", now)
	running.Status = wire.WorkflowRunStatusRunning
	running.CompletedAt = nil
	testutil.FailErr(t, "update running run", h.RunStore.State.Update(t.Context(), running))
	h.SeedRunReport(t, running)
	rec = h.GetReport(t, running.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("nonterminal status = %d body = %s", rec.Code, rec.Body.String())
	}
	contractfixture.AssertErrorCode(t, rec, "report_not_found")
}

func TestGetWorkflowRunReport_ArtifactsFromOverlay(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t)
	dataDir := t.TempDir()
	lookup := func(_ context.Context, _ string) (string, error) { return h.Project.ID, nil }
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
		Records:      visual.NewRecords(h.SqlDB, eventoutbox.New(h.SqlDB, nil), visual.ArtifactProjection{}),
	})
	h.RestartWithVisualStore(hot)

	run := h.SeedSecuritySurveyRun(t, "run_report_arts_001")
	h.SeedRunReport(t, run)
	h.SeedVerdict(t, run)
	h.SeedFindings(t, run)

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
	h.SeedToolVisual(t, run, renderArt)
	h.SeedToolVisual(t, run, captureArt)

	// Restart: new DurableStore over the same overlay (empty hot tier).
	cold := visual.NewDurableStore(visual.DurableConfig{
		ArtifactsDir: artifactsDir,
		Lookup:       lookup,
		Records:      visual.NewRecords(h.SqlDB, eventoutbox.New(h.SqlDB, nil), visual.ArtifactProjection{}),
	})
	h.RestartWithVisualStore(cold)

	input, ok, err := h.Srv.Admin.Workflow.Reports.BuildRunReportInput(t.Context(), run.ID)
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

	rec := h.GetReport(t, run.ID)
	contractfixture.AssertPDFOK(t, rec)
}

func TestGetWorkflowRunReport_MissingArtifactBytesShrinks(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t, func(d *hostapi.Dependencies) { d.Source.VisualStore = visual.NewMemoryStore() })
	run := h.SeedSecuritySurveyRun(t, "run_report_missing_art")
	h.SeedRunReport(t, run)
	h.SeedToolVisual(t, run, wire.VisualArtifact{
		ID: "art_unavailable", Mime: "image/png", Source: wire.VisualArtifactSourceRender,
		Caption: "gone", StoreRef: true,
	})

	input, ok, err := h.Srv.Admin.Workflow.Reports.BuildRunReportInput(t.Context(), run.ID)
	testutil.FailErr(t, "buildRunReportInput", err)
	if !ok {
		t.Fatal("want report available")
	}
	if len(input.Artifacts) != 0 {
		t.Fatalf("missing artifact must be omitted; got %+v", input.Artifacts)
	}
	rec := h.GetReport(t, run.ID)
	contractfixture.AssertPDFOK(t, rec)
}

// A review paused as review_blocked serves its retained snapshot as an
// incomplete report; a run paused for any other reason has no report yet.
func TestBlockedReviewServesItsRetainedSnapshot(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t)
	run := h.SeedSecuritySurveyRun(t, "run_review_blocked")
	repair := workflow.ReviewRepair{
		ID: "repair", Phase: "claims", State: "blocked", UpdatedAt: time.Now().UTC(),
		Responses: []workflow.ReviewRepairResponse{{ID: "response"}},
		Snapshot:  &workflow.ReviewSnapshot{Vars: map[string]any{}, Unavailable: []string{"scan ledger"}},
	}
	_, err := h.WfMgr.StampRunVars(t.Context(), run.ID, func(_ context.Context, _ *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		vars["review_repairs"] = []workflow.ReviewRepair{repair}
		return vars, true, nil
	})
	testutil.FailErr(t, "stamp blocked repair", err)
	pause := func(reason string) {
		t.Helper()
		current, err := h.RunStore.Store.Runs.Get(t.Context(), run.ID)
		testutil.FailErr(t, "get run", err)
		current.Status, current.PauseReason, current.CurrentPhase, current.CompletedAt = wire.WorkflowRunStatusPaused, reason, "claims", nil
		testutil.FailErr(t, "pause run", h.RunStore.State.Update(t.Context(), current))
	}

	reports := h.Srv.Admin.Workflow.Reports
	pause(workflow.ReviewBlockedReason)
	input, ok, err := reports.BuildRunReportInput(t.Context(), run.ID)
	testutil.FailErr(t, "build blocked report", err)
	if !ok || input.Kind != report.BlockedReviewSnapshot || input.Completeness() != report.CompletenessIncomplete {
		t.Fatalf("blocked review report = ok %v kind %q", ok, input.Kind)
	}
	if !strings.Contains(input.Synthesis, "Unavailable when paused: scan ledger.") {
		t.Fatalf("snapshot omitted its unavailable sources: %s", input.Synthesis)
	}
	contractfixture.AssertPDFOK(t, h.GetReport(t, run.ID))

	pause("user")
	if _, ok, err := reports.BuildRunReportInput(t.Context(), run.ID); err != nil || ok {
		t.Fatalf("an ordinarily paused run offered a report: ok %v err %v", ok, err)
	}
}

func TestReportDownloadRequiresRecordedDelivery(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t)
	run := h.SeedSecuritySurveyRun(t, "run_no_delivery")
	h.SeedRunReport(t, run)
	_, err := h.WfMgr.StampRunVars(t.Context(), run.ID, func(_ context.Context, _ *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		return workflowrunstate.SetGateSatisfied(vars, "topology_report_delivered", false), true, nil
	})
	testutil.FailErr(t, "clear delivery", err)
	rec := h.GetReport(t, run.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("undelivered report status = %d", rec.Code)
	}
	contractfixture.AssertErrorCode(t, rec, "report_not_found")
}
