package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestGetSessionPendingWorkflowStartOpenAPI(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	dir := t.TempDir()
	sess := createSessionHTTP(t, srv, dir)

	exitAmbientRunHTTP(t, srv, sess.ID)
	active, err := h.WorkflowMgr.GetActive(t.Context(), sess.ID)
	if err != nil || active != nil {
		t.Fatalf("ambient exit left active workflow: %+v; error=%v", active, err)
	}

	if err := h.WorkflowMgr.NoteWorkflowStartProposal(context.Background(), sess.ID, "plan", "1.0.0"); err != nil {
		t.Fatalf("NoteWorkflowStartProposal: %v", err)
	}

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}", map[string]string{"id": sess.ID})

	var body api.Session
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if body.UI == nil || body.UI.PendingWorkflowStart == nil {
		t.Fatal("expected ui.pending_workflow_start")
	}
	if body.UI.PendingWorkflowStart.WorkflowID != "plan" {
		t.Fatalf("workflow_id = %q", body.UI.PendingWorkflowStart.WorkflowID)
	}
}

func TestGetActiveWorkflowRunHumanApprovalAwaitingOpenAPI(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	blueprintMgr := h.BlueprintMgr
	sess := createSessionHTTP(t, srv, t.TempDir())

	startBody := planStartBody
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run api.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode workflow run: %v", err)
	}
	seedPlanStub(t, blueprintMgr, run.ProjectID, run.BlueprintPath)
	run = advancePlanToExpandHTTP(t, h, run)
	run = advancePlanRunHTTP(t, srv, run.ID)
	if run.CurrentPhase == "review" {
		run = completePlanDepthAtNoneHTTP(t, h, run.ID, "review")
	}
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve before UI assertion", run.CurrentPhase)
	}

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs/active", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET active status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}/workflow-runs/active", map[string]string{"id": sess.ID})

	active := decodeActiveWorkflowRun(t, w.Body.Bytes())
	if active == nil || active.UI == nil || !active.UI.HumanApprovalAwaiting {
		t.Fatal("expected ui.human_approval_awaiting at approve with valid unapproved plan")
	}
}
