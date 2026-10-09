package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestAdvanceBlockedUntilTopologyStageMarked(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "topo-gate",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                "implement",
			CompleteWhen:      "topology_stage_complete",
			BindTopologyStage: "implement",
			Next:              "done",
		}, {ID: "done"}},
	})
	h, sess := buildWorkflowHarnessWithManifest(t, manifest)
	srv := h.Server
	workflowMgr := h.WorkflowMgr
	run := startHarnessRun(t, h, sess.ID, "topo-gate", "1.0.0")

	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("advance before stage complete status = %d body = %s", w.Code, w.Body.String())
	}

	if err := workflowMgr.Phases.MarkTopologyStageComplete(t.Context(), run.ID, "implement", "", ""); err != nil {
		testutil.FailErr(t, "workflowMgr.Phases.MarkTopologyStageComplete failed", err)
	}
	req = authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("advance after stage complete status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestUserDecisionAPIBlocksCasualChatAdvance(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "decision-api",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "confirm",
			CompleteWhen:       "user_decision:confirm,yes",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto,
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{
					Prompt:       "Proceed?",
					ResponseType: workflowdef.FeedbackResponseSingleChoice,
					Options:      []string{"yes", "no"},
				},
			},
		}},
	})
	h, sess := buildWorkflowHarnessWithManifest(t, manifest)
	srv := h.Server
	run := startHarnessRun(t, h, sess.ID, "decision-api", "1.0.0")

	acceptPromptHTTP(t, srv, sess.ID, "yes please")
	waitTranscriptContainsHTTP(t, srv, sess.ID, "yes please", promptIdleBudget)

	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("advance after chat status = %d body = %s", w.Code, w.Body.String())
	}

	req = authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/decisions/confirm", workflowCommandBody(t, srv, run.ID, map[string]any{"choice": "yes"}))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("decision resolve status = %d body = %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if run.Status != wire.WorkflowRunStatusComplete {
		t.Fatalf("expected auto-advance to complete run after decision, status=%q phase=%q", run.Status, run.CurrentPhase)
	}
}
