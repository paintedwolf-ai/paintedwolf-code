package workflow

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestFanoutCoverageAccountsForEveryLegAndRecovery(t *testing.T) {
	plan := FanoutPlan{MaxAttempts: 2, Legs: []FanoutPlanLeg{{ID: "leg-1", AgentType: "reviewer"}, {ID: "leg-2", AgentType: "reviewer"}}}
	job := func(id, leg, status string, at int64) api.WorkerTask {
		return api.WorkerTask{ID: id, WorkflowPhase: "execute", WorkflowWorkID: leg, Status: api.WorkerStatusComplete, CreatedAt: time.Unix(at, 0), Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: status}}}
	}
	tasks := []api.WorkerTask{job("one", "leg-1", "partial", 1)}
	got := FanoutCoverage(plan, tasks, "execute")
	if got[0].Settled || got[1].Settled || got[1].Status != "not_started" {
		t.Fatalf("premature completion: %#v", got)
	}
	tasks = append(tasks, job("two", "leg-2", "complete", 2))
	got = FanoutCoverage(plan, tasks, "execute")
	if got[0].Settled || !got[1].Settled {
		t.Fatalf("last success hid partial peer: %#v", got)
	}
	tasks = append(tasks, job("retry", "leg-1", "partial", 3))
	got = FanoutCoverage(plan, tasks, "execute")
	if !got[0].Settled || got[0].Status != "partial" || len(got[0].Attempts) != 2 {
		t.Fatalf("recovery accounting: %#v", got)
	}
	if wrong := FanoutCoverage(plan, tasks, "other"); wrong[0].Status != "not_started" {
		t.Fatalf("cross-phase contamination: %#v", wrong)
	}
	tasks[2].Status = api.WorkerStatusFailed
	tasks[2].Result.CompletionReport.LegStatus = "complete"
	if failed := FanoutCoverage(plan, tasks, "execute"); failed[0].Status != "failed" || !failed[0].Settled {
		t.Fatalf("model completion masked failed execution: %#v", failed)
	}
}
