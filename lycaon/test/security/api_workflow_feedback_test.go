package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestFeedbackSubmitUnblocksAdvance(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "feedback-submit",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "clarify",
			CompleteWhen:       "user_feedback_received:clarify",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto,
			Next:               "done",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "REST or GraphQL?"},
			},
		}, {ID: "done"}},
	})
	h, sess := buildWorkflowHarnessWithManifest(t, manifest)
	srv := h.Server
	run := startHarnessRun(t, h, sess.ID, "feedback-submit", "1.0.0")

	before, err := h.Workflows.Manager.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "get run before advance", err)
	adv := httptest.NewRecorder()
	srv.ServeHTTP(adv, authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil)))
	if adv.Code != http.StatusConflict {
		t.Fatalf("advance before feedback status = %d body = %s", adv.Code, adv.Body.String())
	}
	assertPhaseGateError(t, adv.Body.Bytes(), "clarify", "user_feedback_received:clarify")
	held, err := h.Workflows.Manager.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "get run after refused advance", err)
	if held.CurrentPhase != before.CurrentPhase || held.Status != before.Status {
		t.Fatalf("refused advance moved the run: %s/%s, want %s/%s", held.CurrentPhase, held.Status, before.CurrentPhase, before.Status)
	}

	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/feedback/clarify", workflowCommandBody(t, srv, run.ID, map[string]any{"response": "Use REST"}))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("feedback submit status = %d body = %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done after feedback auto-advance", run.CurrentPhase)
	}
}
