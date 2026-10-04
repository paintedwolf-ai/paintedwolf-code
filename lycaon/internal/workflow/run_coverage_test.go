package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoverageFactsDeduplicateMovedFilesAndFenceRescans(t *testing.T) {
	var scans []api.CodeScan
	for _, scanner := range []string{"sast", "sca", "secrets"} {
		scans = append(scans, api.CodeScan{ID: scanner, ScannerID: scanner, Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoveragePartial, CreatedAt: time.Unix(1, 0), Warnings: []api.ScanWarning{{Kind: api.ScanWarningSourceMoved, File: "a.go"}}})
	}
	facts := BuildCoverageFacts(workflowdef.Manifest{}, nil, nil, scans)
	if len(facts.Gaps) != 1 || facts.Gaps[0].Count != 1 || len(facts.Gaps[0].Scans) != 3 {
		t.Fatalf("distinct source gap = %+v", facts.Gaps)
	}
	reordered := BuildCoverageFacts(workflowdef.Manifest{}, nil, nil, []api.CodeScan{scans[2], scans[0], scans[1]})
	if reordered.Revision != facts.Revision {
		t.Fatal("scan ordering changed review revision")
	}
	scans = append(scans, api.CodeScan{ID: "retry", ScannerID: "sast", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete, CreatedAt: time.Unix(2, 0)})
	current := BuildCoverageFacts(workflowdef.Manifest{}, nil, nil, scans)
	if current.Revision == facts.Revision || len(current.Gaps[0].Scans) != 2 {
		t.Fatal("rescan neither credited nor revision fenced")
	}
}

func TestCoverageVerdictCitationsJoinGrounding(t *testing.T) {
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"coverage": workflowdef.VerdictCoverageType}}
	review := api.CoverageReview{Revision: "current", Assessments: []api.CoverageAssessment{{ID: "auth", CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "file#1"}}}}}
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	got := allVerdictCitations(def, map[string]string{"coverage": string(raw)}, []api.CitationGroundingCitedEvidence{{Handle: "scan#1"}})
	if len(got) != 2 || got[1].Handle != "file#1" {
		t.Fatalf("coverage citations bypassed audit: %+v", got)
	}
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"unexpected":true}`} {
		if _, err := ParseVerdictCoverage(def, map[string]string{"coverage": raw}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestCoverageAdmissionRejectsStaleReviewWithoutSpendingRound(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	manifest := reviewLoopTestManifest()
	manifest.PhaseDefs[0].ReviewLoop.VerdictSchema = map[string]string{"verdict": "SELECTED", "coverage": workflowdef.VerdictCoverageType}
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	mgr.Inventory = fakeInventory{}
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return nil, nil }
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	out, err := mgr.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": `{"revision":"stale","assessments":[]}`}, nil, nil)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	if out.Valid || out.Terminal || out.CoverageIssue == "" {
		t.Fatalf("stale coverage advanced: %+v", out)
	}
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	if ReviewLoopAttempt(vars, "judge") != 0 {
		t.Fatal("coverage refusal consumed review budget")
	}
	facts, err := mgr.CoverageFacts(t.Context(), run, manifest)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	out, err = mgr.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	if !out.Terminal {
		t.Fatalf("current complete accounting refused: %+v", out)
	}
}

func TestCoverageRevisionBindsThePlannedQuestionAndThreatModel(t *testing.T) {
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "execute", Gates: []string{"worker_cycle_ready"}}}}
	plan := FanoutPlan{Phase: "execute", ThreatModel: "Untrusted clients", Legs: []FanoutPlanLeg{{ID: "boundary", Subject: "API", Prompt: "Trace authorization", Scope: &api.TaskScope{Paths: []string{"internal/api"}}}}}
	facts := BuildCoverageFacts(manifest, stampFanoutPlan(nil, plan), nil, nil)
	if len(facts.Obligations) != 1 || !facts.Obligations[0].Blocking || facts.Obligations[0].Question != plan.Legs[0].Prompt || facts.Obligations[0].Scope == nil {
		t.Fatalf("planned question lost: %+v", facts)
	}
	plan.ThreatModel = "Untrusted project files"
	changed := BuildCoverageFacts(manifest, stampFanoutPlan(nil, plan), nil, nil)
	if changed.Revision == facts.Revision {
		t.Fatal("changed threat model retained an old coverage judgment")
	}
}

func TestCoverageRevisionTracksReviewWorkNotReportProduction(t *testing.T) {
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{
		{ID: "execute"},
		{ID: "challenge", ReviewLoop: &workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"coverage": workflowdef.VerdictCoverageType}}},
		{ID: "synthesis"},
	}}
	base := []api.WorkerTask{{ID: "review", WorkflowPhase: "challenge", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}
	facts := BuildCoverageFacts(manifest, nil, base, nil)
	for _, status := range []api.WorkerStatus{api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusFailed, api.WorkerStatusComplete} {
		tasks := append(append([]api.WorkerTask(nil), base...), api.WorkerTask{ID: "report-worker", WorkflowPhase: "synthesis", Status: status})
		if got := BuildCoverageFacts(manifest, nil, tasks, nil); got.Revision != facts.Revision {
			t.Fatalf("report worker %s invalidated accepted review", status)
		}
		tasks[1].WorkflowPhase = "challenge"
		if got := BuildCoverageFacts(manifest, nil, tasks, nil); got.Revision == facts.Revision {
			t.Fatalf("new review work %s did not invalidate review", status)
		}
	}
}

func TestCoverageInjectionOnlyRunsAtReviewBoundary(t *testing.T) {
	mgr := &RunManager{WorkerTasks: func(context.Context, string) ([]api.WorkerTask, error) {
		t.Fatal("non-review phase loaded the coverage worker ledger")
		return nil, nil
	}}
	manifest := workflowdef.Manifest{Phases: []string{"execute", "challenge", "synthesis"}, PhaseDefs: []workflowdef.PhaseDef{
		{ID: "execute"},
		{ID: "challenge", ReviewLoop: &workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"coverage": workflowdef.VerdictCoverageType}}},
		{ID: "synthesis"},
	}}
	for _, phase := range []string{"execute", "synthesis"} {
		snap := mgr.workflowRuntimeSnapshot(t.Context(), &api.WorkflowRun{ID: "run", CurrentPhase: phase}, manifest, nil)
		if snap.CoverageReview != "" {
			t.Fatalf("%s received review-only coverage facts", phase)
		}
	}
}
