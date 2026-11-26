package workerruntime

import (
	"context"
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

func TestUnreadableObligationStopsTheWakeAndReportsLookupFailure(t *testing.T) {
	db := testdbfixture.Open(t, "obligation.db")
	store := persistence.New(db)
	r := &Runtime{}
	r.SetDependencies(Dependencies{Workflows: &workflows.Runtime{Manager: &workflow.RunManager{Obligations: &workflow.Obligations{Runs: store.Runs}}}})
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	ctx, a := actionoutcome.Capture(cancelled, api.ContinuationActionWorkflowResume)
	if !r.parkWorkflowObligation(ctx, "chat", false) {
		t.Fatal("unreadable obligation allowed a wake")
	}
	var sessions actionoutcome.Sessions
	s := sessions.For("chat")
	a.Commit(s, "chat", nil)
	out, err := s.Actions.Await(t.Context(), a.ID())
	testutil.FailErr(t, "await obligation lookup", err)
	if out.State != api.ContinuationStateFailed || out.FailureCode != string(api.HostContinuationFailureHostLookupFailed) || out.TurnID != "" {
		t.Fatalf("obligation lookup outcome = %+v", out)
	}
}
