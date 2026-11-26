package workerruntime

import (
	"testing"

	"github.com/lycaon/lycaon/internal/actionoutcome"
	"github.com/lycaon/lycaon/internal/app/workflows"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStaleResumeAndApprovalReportInactiveRun(t *testing.T) {
	db := testdbfixture.Open(t, "inactive.db")
	r := &Runtime{}
	r.SetDependencies(Dependencies{Workflows: &workflows.Runtime{Manager: &workflow.RunManager{Store: persistence.New(db)}}})
	run := &api.WorkflowRun{ID: "retired", SessionID: "idle"}
	var sessions actionoutcome.Sessions
	s := sessions.For(run.SessionID)
	for _, kind := range []api.ContinuationAction{api.ContinuationActionWorkflowResume, api.ContinuationActionPlanApproval} {
		ctx, a := actionoutcome.Capture(t.Context(), kind)
		if kind == api.ContinuationActionWorkflowResume {
			r.OnWorkflowRunResumed(ctx, run)
		} else {
			r.OnWorkflowHumanApprovalAdvanced(ctx, run)
		}
		a.Commit(s, run.SessionID, nil)
		out, err := s.Actions.Await(ctx, a.ID())
		testutil.FailErr(t, "await inactive hook", err)
		if out.State != api.ContinuationStateNotContinued || out.Reason != api.ContinuationReasonRunInactive {
			t.Fatalf("%s: %+v", kind, out)
		}
	}
}
