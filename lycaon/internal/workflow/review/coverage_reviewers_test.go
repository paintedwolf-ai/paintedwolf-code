package review_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

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
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	mgr.Coverage.Inventory = fakeInventory{}
	workflowTaskQuery1 := func(context.Context, string) ([]api.WorkerTask, error) { return nil, nil }
	mgr.Fanout.WorkerTasks = workflowTaskQuery1
	mgr.Coverage.WorkerTasks = workflowTaskQuery1
	mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery1
	mgr.Verdicts.WorkerTasks = workflowTaskQuery1
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start coverage run", err)
	facts, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "load candidate scope", err)
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	testutil.FailErr(t, "encode candidate", err)
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	testutil.FailErr(t, "record candidate", err)
	if !out.Terminal {
		t.Fatalf("candidate did not settle: %+v", out)
	}
	run, err = mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "load review phase", err)
	if run.CurrentPhase != "judge" {
		t.Fatalf("phase=%s", run.CurrentPhase)
	}
	assignment, err := mgr.Coverage.CoverageAssignment(t.Context(), run, manifest, "auditor")
	testutil.FailErr(t, "load independent assignment", err)
	task := &api.WorkerTask{WorkflowRunID: run.ID, WorkflowPhase: "judge", AgentType: "auditor"}
	err = mgr.Coverage.ValidateCoverageCompletion(t.Context(), task, nil)
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil || rejected.Code != "COMPLETE_LEG_COVERAGE_INVALID" || rejected.Data["assignment"] == nil {
		t.Fatalf("missing structured repair: %v", err)
	}
	review := &api.CoverageReview{Revision: assignment.Facts.Revision, Assessments: []api.CoverageAssessment{}}
	testutil.FailErr(t, "accept current worker assessment", mgr.Coverage.ValidateCoverageCompletion(t.Context(), task, review))
	mgr.Coverage.Inventory = fakeInventory{run: []api.CodeScan{{ID: "changed", Status: api.CodeScanStatusComplete, Warnings: []api.ScanWarning{{Kind: "file_partial_semantics", File: "service/main.go"}}}}}
	if err := mgr.Coverage.ValidateCoverageCompletion(t.Context(), task, review); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("changed scope not repaired: %v", err)
	}
	task.AgentType = "other"
	testutil.FailErr(t, "leave ordinary worker unconstrained", mgr.Coverage.ValidateCoverageCompletion(t.Context(), task, nil))
}
