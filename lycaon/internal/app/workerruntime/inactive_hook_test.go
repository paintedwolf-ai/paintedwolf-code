package workerruntime_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/actionoutcome"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestStaleResumeAndApprovalReportInactiveRun(t *testing.T) {
	h := wiring.BuildForTest(t)
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, h.ProjectDir(t, "inactive"))
	testutil.FailErr(t, "create idle session", err)
	run := &api.WorkflowRun{ID: "retired", SessionID: sess.ID}
	s := h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Outcomes.For(sess.ID)
	for _, kind := range []api.ContinuationAction{api.ContinuationActionWorkflowResume, api.ContinuationActionPlanApproval} {
		ctx, a := actionoutcome.Capture(t.Context(), kind)
		if kind == api.ContinuationActionWorkflowResume {
			h.Workers.OnWorkflowRunResumed(ctx, run)
		} else {
			h.Workers.OnWorkflowHumanApprovalAdvanced(ctx, run)
		}
		a.Commit(s, sess.ID, nil)
		out, err := s.Actions.Await(ctx, a.ID())
		testutil.FailErr(t, "await inactive hook", err)
		if out.State != api.ContinuationStateNotContinued || out.Reason != api.ContinuationReasonRunInactive {
			t.Fatalf("%s: %+v", kind, out)
		}
	}
}
