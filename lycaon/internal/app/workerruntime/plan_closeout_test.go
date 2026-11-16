package workerruntime

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerplan/plantest"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// A session's closeout waits for its planned workers: an unstarted planned
// worker holds it, and withdrawing that worker releases it. The settlement
// hooks leave a plan with no workflow run alone.
func TestPlanCloseoutWaitsForPlannedWorkers(t *testing.T) {
	database := testdbfixture.Open(t, "plan-closeout.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	runtime := New(database, nil, worker.WorkersConfig{})
	runtime.Plan = plantest.New(t, plantest.Deps(runtime.Records))
	actor := workerrecords.Actor{SessionID: "parent", Role: workerrecords.RoleCoordinator, PrincipalID: "parent"}
	complete := func() bool {
		t.Helper()
		ok, err := runtime.PlanCloseoutComplete(t.Context(), "parent")
		testutil.FailErr(t, "plan closeout", err)
		return ok
	}

	if !complete() {
		t.Fatal("a session with nothing planned is not closed out")
	}
	planned, err := runtime.Records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "plan"}, workerrecords.WorkerSpec{
		Name: "leg", AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite,
		WorkflowWorkID: "leg", BlueprintPath: ".lycaon/plans/p.md",
	})
	testutil.FailErr(t, "plan worker", err)
	if complete() {
		t.Fatal("an unstarted planned worker did not hold closeout")
	}

	// Neither settlement hook has a workflow run to advance yet.
	runtime.NotifyPromotedTask(t.Context(), api.WorkerTask{ParentSessionID: "parent"})
	testutil.FailErr(t, "scan closeout", runtime.OnScanCloseout(t.Context(), api.CodeScan{SessionID: "parent"}))
	if complete() {
		t.Fatal("a settlement hook closed out an unsettled plan")
	}

	_, err = runtime.Records.Close(t.Context(), actor, workerrecords.Op{ID: "withdraw"}, planned.WorkerID, "withdrawn")
	testutil.FailErr(t, "withdraw", err)
	if !complete() {
		t.Fatal("withdrawing the planned worker did not release closeout")
	}
	runtime.NotifySettledPlanCloseout(t.Context(), "parent")
}
