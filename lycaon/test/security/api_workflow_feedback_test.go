package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestFeedbackPhaseBlocksAdvanceUntilSubmit(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "feedback-api",
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
	run := startHarnessRun(t, h, sess.ID, "feedback-api", "1.0.0")

	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("advance before feedback status = %d body = %s", w.Code, w.Body.String())
	}
}

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
