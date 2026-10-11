package review

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
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

func TestCoverageRevisionBindsThePlannedQuestionAndThreatModel(t *testing.T) {
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "execute", Gates: []string{"worker_cycle_ready"}}}}
	plan := runstate.FanoutPlan{Phase: "execute", ThreatModel: "Untrusted clients", Legs: []runstate.FanoutPlanLeg{{ID: "boundary", Subject: "API", Prompt: "Trace authorization", Scope: &api.TaskScope{Paths: []string{"internal/api"}}}}}
	facts := BuildCoverageFacts(manifest, runstate.StampFanoutPlan(nil, plan), nil, nil)
	if len(facts.Obligations) != 1 || !facts.Obligations[0].Blocking || facts.Obligations[0].Question != plan.Legs[0].Prompt || facts.Obligations[0].Scope == nil {
		t.Fatalf("planned question lost: %+v", facts)
	}
	plan.ThreatModel = "Untrusted project files"
	changed := BuildCoverageFacts(manifest, runstate.StampFanoutPlan(nil, plan), nil, nil)
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
