package workflowadmin_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	def "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkflowActionsReturnTheirOwnContinuation(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	h.RegisterManifest(def.Manifest{ID: "clarification", Version: "1.0.0", PhaseDefs: []def.PhaseDef{
		{ID: "clarify", CompleteWhen: "user_feedback_received:clarify", AdvanceWhenGateMet: def.AdvanceWhenGateMetAuto, Next: "confirm",
			OnEnter: def.PhaseOnEnter{RequestUserFeedback: &def.UserFeedbackPrompt{Prompt: "Which database?"}}},
		{ID: "confirm", CompleteWhen: "user_decision:confirm,yes", AdvanceWhenGateMet: def.AdvanceWhenGateMetAuto, Next: "done",
			OnEnter: def.PhaseOnEnter{RequestUserFeedback: &def.UserFeedbackPrompt{Prompt: "Proceed?", ResponseType: def.FeedbackResponseSingleChoice, Options: []string{"yes", "no"}}}},
		{ID: "done", Terminal: true},
	}})
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, h.ProjectDir(t, "project"))
	testutil.FailErr(t, "create session", err)
	ctx := h.OwnerCtx(t, t.Context())
	router := workflowRouter(h.Server.Admin.Workflow)
	post := func(path, body string, status int) wire.WorkflowRun {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return decodeOK[wire.WorkflowRun](t, rec, status)
	}
	outcomes := h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Outcomes.For(sess.ID)
	seen := map[string]bool{}
	check := func(run wire.WorkflowRun, kind wire.ContinuationAction, state wire.ContinuationState) {
		t.Helper()
		action, err := outcomes.Actions.Await(ctx, run.ContinuationID)
		testutil.FailErr(t, "await "+string(kind), err)
		if seen[action.ContinuationID] || action.Action != kind || action.State != state || action.SessionID != sess.ID {
			t.Fatalf("%s: %+v", kind, action)
		}
		seen[action.ContinuationID] = true
	}
	run := post("/v1/sessions/"+sess.ID+"/workflow-runs", fmt.Sprintf(`{"workflow_id":"clarification","workflow_version":"1.0.0","operation_id":%q,"request":"Choose a database"}`, uuid.NewString()), http.StatusCreated)
	check(run, wire.ContinuationActionWorkflowStart, wire.ContinuationStateHeld)
	path := "/v1/workflow-runs/" + run.ID
	run = post(path+"/pause", fmt.Sprintf(`{"expected_revision":%d}`, run.Revision), http.StatusOK)
	run = post(path+"/resume", fmt.Sprintf(`{"expected_revision":%d}`, run.Revision), http.StatusOK)
	check(run, wire.ContinuationActionWorkflowResume, wire.ContinuationStateHeld)
	run = post(path+"/feedback/clarify", fmt.Sprintf(`{"response":"SQLite","expected_revision":%d}`, run.Revision), http.StatusOK)
	check(run, wire.ContinuationActionFeedback, wire.ContinuationStateHeld)
	run = post(path+"/decisions/confirm", fmt.Sprintf(`{"choice":"yes","expected_revision":%d}`, run.Revision), http.StatusOK)
	check(run, wire.ContinuationActionFeedback, wire.ContinuationStateFinished)
}
