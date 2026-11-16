package workerplan_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerplan/plantest"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// With nothing approved against it, no run active, no diff and no receipts,
// a plan-bound worker is admitted, its result fails exactly the criteria that
// need evidence, and closeout says so.
func TestPlanBoundWorkWithNoEvidenceFailsOnlyItsEvidenceCriteria(t *testing.T) {
	env := newPlanEnv(t)
	deps := plantest.Deps(env.records)
	deps.Jobs = env.jobs
	env.svc = plantest.New(t, deps)

	_, err := env.plan(t, "missing", ".lycaon/plans/absent.md")
	wantReject(t, err, "blueprint_not_found")

	env.workflowRun(t, "run-1", "work")
	settled := env.worker(t, workerrecords.WorkerSpec{
		Name: "settled", WorkflowRunID: "run-1", WorkflowPhase: "work", WorkflowWorkID: "leg-a", BlueprintPath: ".lycaon/plans/p.md",
	})
	testutil.FailErr(t, "admit", env.svc.CheckRoundAdmission(t.Context(), workerplan.RoundAdmissionRequest{
		Actor: env.coord, Tool: "task", ProjectID: testdbseed.DefaultProjectID, Worker: settled,
	}))
	env.startRound(t, settled.ID, "job-a")
	env.settleRound(t, "job-a")
	env.jobs["job-a"] = &api.WorkerTask{ID: "job-a", Status: api.WorkerStatusComplete}
	verdict, err := env.svc.CheckSessionCloseout(t.Context(), env.coord.SessionID, "/repo")
	testutil.FailErr(t, "closeout", err)
	if !verdict.OK {
		t.Fatalf("verdict = %+v for a settled worker with no criteria", verdict)
	}

	criteria := []string{"file:modified:pkg/a.go", "test:pass:go test ./pkg/..."}
	judged := env.worker(t, workerrecords.WorkerSpec{
		Name: "judged", ScopeMode: workerrecords.ScopeRead, ChildSessionID: "", BaseSHA: "abc1234", Criteria: criteria,
	})
	env.startRound(t, judged.ID, "job-b")
	outcomes, err := env.svc.EvaluateRoundDelivery(t.Context(), env.coord.SessionID, "job-b", workerplan.DeliveryInput{
		ChildSessionID: "child", ProjectDir: "/repo", JobComplete: true, Summary: "Done.",
	})
	testutil.FailErr(t, "evaluate", err)
	want := []workerrecords.CriterionOutcome{
		{Criterion: criteria[0], Detail: "file_unmodified"},
		{Criterion: criteria[1], Detail: "run_missing"},
	}
	if !slices.Equal(outcomes, want) {
		t.Fatalf("outcomes = %+v, want %+v", outcomes, want)
	}
}
