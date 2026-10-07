package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoverageReviewerAdmissionRequiresCurrentExplicitOutcomes(t *testing.T) {
	run := &api.WorkflowRun{ID: "run", CurrentPhase: "check"}
	def := workflowdef.ReviewLoopDef{CoverageReviewers: []string{"auditor"}}
	facts := reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "area"}}, Gaps: []reviewcoverage.Fact{{ID: "gap"}}}
	facts.Seal()
	review := api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{
		{ID: "area", Disposition: "satisfied", Reason: "Traced", CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}},
		{ID: "gap", Disposition: "material_open", Reason: "Unresolved scope", Obligations: []string{"area"}, CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}},
	}}
	task := api.WorkerTask{ID: "review", WorkflowRunID: "run", WorkflowPhase: "check", AgentType: "auditor", CreatedAt: time.Unix(1, 0), Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &review}}}
	if err := validateCoverageReviewerResults(run, def, facts, []api.WorkerTask{task}); err != nil {
		t.Fatalf("explicit disagreement rejected: %v", err)
	}
	for _, tc := range []struct {
		name  string
		alter func(*api.WorkerTask, *api.CoverageReview)
	}{
		{"no coverage", func(task *api.WorkerTask, _ *api.CoverageReview) { task.Result.CompletionReport.CoverageReview = nil }},
		{"stale", func(_ *api.WorkerTask, r *api.CoverageReview) { r.Revision = "stale" }},
		{"omitted gap", func(_ *api.WorkerTask, r *api.CoverageReview) { r.Assessments = r.Assessments[:1] }},
		{"wrong phase", func(task *api.WorkerTask, _ *api.CoverageReview) { task.WorkflowPhase = "earlier" }},
		{"failed", func(task *api.WorkerTask, _ *api.CoverageReview) { task.Status = api.WorkerStatusFailed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := review
			copy := task
			copy.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &r}}
			tc.alter(&copy, &r)
			if err := validateCoverageReviewerResults(run, def, facts, []api.WorkerTask{copy}); err == nil {
				t.Fatal("invalid reviewer accepted")
			}
		})
	}
	later := task
	later.ID = "later"
	later.CreatedAt = time.Unix(2, 0)
	later.Status = api.WorkerStatusFailed
	if err := validateCoverageReviewerResults(run, def, facts, []api.WorkerTask{task, later}); err == nil {
		t.Fatal("earlier result hid failed replacement")
	}
	if err := validateCoverageReviewerResults(run, workflowdef.ReviewLoopDef{}, facts, nil); err != nil {
		t.Fatal("undeclared workflow acquired reviewer gate")
	}
}

func TestCoverageCompletionRepairsAgainstCurrentHostAssignment(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	manifest := reviewLoopTestManifest()
	candidate := manifest.PhaseDefs[0]
	candidate.ID = "candidate"
	candidate.Next = "judge"
	candidate.ReviewLoop = &workflowdef.ReviewLoopDef{EvidenceKey: "candidate", VerdictSchema: map[string]string{"verdict": "SELECTED", "coverage": workflowdef.VerdictCoverageType}}
	candidate.Gates = []string{"evidence_passed:candidate"}
	judge := manifest.PhaseDefs[0]
	judge.ReviewLoop = &workflowdef.ReviewLoopDef{EvidenceKey: "rl_key", ReconcilesPhase: "candidate", RequiredAgents: []string{"auditor"}, CoverageReviewers: []string{"auditor"}, VerdictSchema: map[string]string{"verdict": "SELECTED", "coverage": workflowdef.VerdictCoverageType}}
	manifest.PhaseDefs = []workflowdef.PhaseDef{candidate, judge, manifest.PhaseDefs[1]}
	manifest = workflowdef.FinalizeManifest(manifest)
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	mgr.Inventory = fakeInventory{}
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return nil, nil }
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start coverage run", err)
	facts, err := mgr.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "load candidate scope", err)
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	testutil.FailErr(t, "encode candidate", err)
	out, err := mgr.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	testutil.FailErr(t, "record candidate", err)
	if !out.Terminal {
		t.Fatalf("candidate did not settle: %+v", out)
	}
	run, err = mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "load review phase", err)
	if run.CurrentPhase != "judge" {
		t.Fatalf("phase=%s", run.CurrentPhase)
	}
	assignment, err := mgr.CoverageAssignment(t.Context(), run, manifest, "auditor")
	testutil.FailErr(t, "load independent assignment", err)
	task := &api.WorkerTask{WorkflowRunID: run.ID, WorkflowPhase: "judge", AgentType: "auditor"}
	err = mgr.ValidateCoverageCompletion(t.Context(), task, nil)
	rejected := tools.AsToolReject(err)
	if rejected == nil || rejected.Code != "COMPLETE_LEG_COVERAGE_INVALID" || rejected.Data["assignment"] == nil {
		t.Fatalf("missing structured repair: %v", err)
	}
	review := &api.CoverageReview{Revision: assignment.Facts.Revision, Assessments: []api.CoverageAssessment{}}
	testutil.FailErr(t, "accept current worker assessment", mgr.ValidateCoverageCompletion(t.Context(), task, review))
	mgr.Inventory = fakeInventory{run: []api.CodeScan{{ID: "changed", Status: api.CodeScanStatusComplete, Warnings: []api.ScanWarning{{Kind: "file_partial_semantics", File: "service/main.go"}}}}}
	if err := mgr.ValidateCoverageCompletion(t.Context(), task, review); tools.AsToolReject(err) == nil {
		t.Fatalf("changed scope not repaired: %v", err)
	}
	task.AgentType = "other"
	testutil.FailErr(t, "leave ordinary worker unconstrained", mgr.ValidateCoverageCompletion(t.Context(), task, nil))
}
