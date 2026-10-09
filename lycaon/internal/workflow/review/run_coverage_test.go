package review_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestCoverageFactsDeduplicateMovedFilesAndFenceRescans(t *testing.T) {
	var scans []api.CodeScan
	for _, scanner := range []string{"sast", "sca", "secrets"} {
		scans = append(scans, api.CodeScan{ID: scanner, ScannerID: scanner, Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoveragePartial, CreatedAt: time.Unix(1, 0), Warnings: []api.ScanWarning{{Kind: api.ScanWarningSourceMoved, File: "a.go"}}})
	}
	facts := workflowreview.BuildCoverageFacts(workflowdef.Manifest{}, nil, nil, scans)
	if len(facts.Gaps) != 1 || facts.Gaps[0].Count != 1 || len(facts.Gaps[0].Scans) != 3 {
		t.Fatalf("distinct source gap = %+v", facts.Gaps)
	}
	reordered := workflowreview.BuildCoverageFacts(workflowdef.Manifest{}, nil, nil, []api.CodeScan{scans[2], scans[0], scans[1]})
	if reordered.Revision != facts.Revision {
		t.Fatal("scan ordering changed review revision")
	}
	scans = append(scans, api.CodeScan{ID: "retry", ScannerID: "sast", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete, CreatedAt: time.Unix(2, 0)})
	current := workflowreview.BuildCoverageFacts(workflowdef.Manifest{}, nil, nil, scans)
	if current.Revision == facts.Revision || len(current.Gaps[0].Scans) != 2 {
		t.Fatal("rescan neither credited nor revision fenced")
	}
}

func TestCoverageAdmissionRejectsStaleReviewWithoutSpendingRound(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	manifest := reviewLoopTestManifest()
	manifest.PhaseDefs[0].ReviewLoop.VerdictSchema = map[string]string{"verdict": "SELECTED", "coverage": workflowdef.VerdictCoverageType}
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	mgr.Coverage.Inventory = fakeInventory{}
	workflowTaskQuery1 := func(context.Context, string) ([]api.WorkerTask, error) { return nil, nil }
	mgr.Fanout.WorkerTasks = workflowTaskQuery1
	mgr.Coverage.WorkerTasks = workflowTaskQuery1
	mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery1
	mgr.Verdicts.WorkerTasks = workflowTaskQuery1
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": `{"revision":"stale","assessments":[]}`}, nil, nil)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	if out.Valid || out.Terminal || out.CoverageIssue == nil || out.CoverageIssue.Code != workflowvalidation.ReviewLoopVerdictInvalidCode {
		t.Fatalf("stale coverage advanced: %+v", out)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	if runstate.ReviewLoopAttempt(vars, "judge") != 0 {
		t.Fatal("coverage refusal consumed review budget")
	}
	facts, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	out, err = mgr.Verdicts.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	if err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	if !out.Terminal {
		t.Fatalf("current complete accounting refused: %+v", out)
	}
}

func TestCoverageAdmissionWaitsForBoundScansAsARejection(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	manifest := reviewLoopTestManifest()
	manifest.PhaseDefs[0].ReviewLoop.VerdictSchema = map[string]string{"verdict": "SELECTED", "coverage": workflowdef.VerdictCoverageType}
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	mgr.Coverage.Inventory = fakeInventory{run: []api.CodeScan{{ID: "scan-1", Status: api.CodeScanStatusRunning}}}
	workflowTaskQuery2 := func(context.Context, string) ([]api.WorkerTask, error) { return nil, nil }
	mgr.Fanout.WorkerTasks = workflowTaskQuery2
	mgr.Coverage.WorkerTasks = workflowTaskQuery2
	mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery2
	mgr.Verdicts.WorkerTasks = workflowTaskQuery2
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start coverage run", err)
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": `{"revision":"any","assessments":[]}`}, nil, nil)
	testutil.FailErr(t, "verdict against pending scans", err)
	if out.Valid || out.Terminal || out.CoverageIssue == nil || out.CoverageIssue.Code != workflowreview.SubmitVerdictScansPendingCode {
		t.Fatalf("pending scans did not reject as %s: %+v", workflowreview.SubmitVerdictScansPendingCode, out)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "scaffold vars", err)
	if runstate.ReviewLoopAttempt(vars, "judge") != 0 {
		t.Fatal("pending-scan refusal consumed review budget")
	}
	if _, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest); !errors.Is(err, runstate.ErrCoverageScansPending) {
		t.Fatalf("CoverageFacts error = %v, want runstate.ErrCoverageScansPending", err)
	}
}

// An independent reviewer seals its assessment against the host assignment,
// whose revision differs from the raw facts; admission must judge it there.
func TestCoverageAdmissionJudgesReviewersAgainstTheirAssignment(t *testing.T) {
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
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	mgr.Coverage.Inventory = fakeInventory{}
	var reviewerTasks []api.WorkerTask
	workflowTaskQuery3 := func(context.Context, string) ([]api.WorkerTask, error) { return reviewerTasks, nil }
	mgr.Fanout.WorkerTasks = workflowTaskQuery3
	mgr.Coverage.WorkerTasks = workflowTaskQuery3
	mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery3
	mgr.Verdicts.WorkerTasks = workflowTaskQuery3
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start coverage run", err)
	facts, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "load facts", err)
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	testutil.FailErr(t, "encode candidate", err)
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	testutil.FailErr(t, "record candidate", err)
	if !out.Terminal {
		t.Fatalf("candidate did not settle: %+v", out)
	}
	run, err = mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "load judge phase", err)
	reviewerTask := func(revision string) api.WorkerTask {
		return api.WorkerTask{ID: "review", WorkflowRunID: run.ID, WorkflowPhase: "judge", AgentType: "auditor", CreatedAt: time.Unix(1, 0), Status: api.WorkerStatusComplete,
			Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: revision, Assessments: []api.CoverageAssessment{}}}}}
	}
	// The reviewer's own leg is part of the judge phase's raw facts but not of
	// the sealed assignment, so the assignment is read first and the
	// coordinator's verdict is built after the reviewer's leg is final.
	reviewerTasks = []api.WorkerTask{reviewerTask("")}
	assignment, err := mgr.Coverage.CoverageAssignment(t.Context(), run, manifest, "auditor")
	testutil.FailErr(t, "load assignment", err)
	judgeVerdict := func() (map[string]string, string) {
		current, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
		testutil.FailErr(t, "load judge facts", err)
		encoded, err := json.Marshal(api.CoverageReview{Revision: current.Revision, Assessments: []api.CoverageAssessment{}})
		testutil.FailErr(t, "encode judge coverage", err)
		return map[string]string{"verdict": "SELECTED", "coverage": string(encoded)}, current.Revision
	}

	reviewerTasks = []api.WorkerTask{reviewerTask(assignment.Facts.Revision)}
	verdict, rawRevision := judgeVerdict()
	if assignment.Facts.Revision == rawRevision {
		t.Fatal("fixture: assignment revision must differ from the raw facts revision")
	}
	reject, err := mgr.Coverage.ValidateReview(t.Context(), run, *judge.ReviewLoop, verdict)
	testutil.FailErr(t, "admit reviewer sealed at the assignment", err)
	if reject != nil {
		t.Fatalf("reviewer assessment sealed at its assignment was refused: %+v", reject.Data)
	}

	reviewerTasks = []api.WorkerTask{reviewerTask(rawRevision)}
	verdict, _ = judgeVerdict()
	reject, err = mgr.Coverage.ValidateReview(t.Context(), run, *judge.ReviewLoop, verdict)
	testutil.FailErr(t, "judge reviewer sealed at the raw facts", err)
	if reject == nil || reject.Code != workflowvalidation.ReviewLoopVerdictInvalidCode {
		t.Fatalf("reviewer assessment sealed outside its assignment was admitted: %+v", reject)
	}
}

func TestCoverageRevisionBindsThePlannedQuestionAndThreatModel(t *testing.T) {
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "execute", Gates: []string{"worker_cycle_ready"}}}}
	plan := runstate.FanoutPlan{Phase: "execute", ThreatModel: "Untrusted clients", Legs: []runstate.FanoutPlanLeg{{ID: "boundary", Subject: "API", Prompt: "Trace authorization", Scope: &api.TaskScope{Paths: []string{"internal/api"}}}}}
	facts := workflowreview.BuildCoverageFacts(manifest, runstate.StampFanoutPlan(nil, plan), nil, nil)
	if len(facts.Obligations) != 1 || !facts.Obligations[0].Blocking || facts.Obligations[0].Question != plan.Legs[0].Prompt || facts.Obligations[0].Scope == nil {
		t.Fatalf("planned question lost: %+v", facts)
	}
	plan.ThreatModel = "Untrusted project files"
	changed := workflowreview.BuildCoverageFacts(manifest, runstate.StampFanoutPlan(nil, plan), nil, nil)
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
	facts := workflowreview.BuildCoverageFacts(manifest, nil, base, nil)
	for _, status := range []api.WorkerStatus{api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusFailed, api.WorkerStatusComplete} {
		tasks := append(append([]api.WorkerTask(nil), base...), api.WorkerTask{ID: "report-worker", WorkflowPhase: "synthesis", Status: status})
		if got := workflowreview.BuildCoverageFacts(manifest, nil, tasks, nil); got.Revision != facts.Revision {
			t.Fatalf("report worker %s invalidated accepted review", status)
		}
		tasks[1].WorkflowPhase = "challenge"
		if got := workflowreview.BuildCoverageFacts(manifest, nil, tasks, nil); got.Revision == facts.Revision {
			t.Fatalf("new review work %s did not invalidate review", status)
		}
	}
}

func TestCoverageInjectionOnlyRunsAtReviewBoundary(t *testing.T) {
	coverage := &workflowreview.Coverage{WorkerTasks: func(context.Context, string) ([]api.WorkerTask, error) {
		t.Fatal("non-review phase loaded the coverage worker ledger")
		return nil, nil
	}}
	manifest := workflowdef.Manifest{Phases: []string{"execute", "challenge", "synthesis"}, PhaseDefs: []workflowdef.PhaseDef{
		{ID: "execute"},
		{ID: "challenge", ReviewLoop: &workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"coverage": workflowdef.VerdictCoverageType}}},
		{ID: "synthesis"},
	}}
	for _, phase := range []string{"execute", "synthesis"} {
		snap := (&workflowruntime.Snapshots{Coverage: coverage}).Project(t.Context(), &api.WorkflowRun{ID: "run", CurrentPhase: phase}, manifest, nil)
		if snap.CoverageReview != "" {
			t.Fatalf("%s received review-only coverage facts", phase)
		}
	}
}
