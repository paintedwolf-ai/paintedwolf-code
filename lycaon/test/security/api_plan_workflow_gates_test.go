package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestPlanWorkflowAdvanceToImplementInvokesChildRun(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	store := h.Store
	blueprintMgr := h.BlueprintMgr
	sess := createSessionHTTP(t, srv, t.TempDir())
	ctx := t.Context()
	if _, err := store.Get(ctx, sess.ID); err != nil {
		testutil.FailErr(t, "store.Get failed", err)
	}

	startBody := planStartBody
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	seedPlanStub(t, blueprintMgr, run.ProjectID, run.BlueprintPath)
	run = advancePlanRunToExecuteHTTP(t, h, srv, blueprintMgr, sess, run)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}
	if run.Status != wire.WorkflowRunStatusPausedOnChild {
		t.Fatalf("status = %q want paused_on_child", run.Status)
	}
	active, err := h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "GetActive", err)
	if active == nil || active.WorkflowID != "implement" {
		t.Fatalf("active child = %+v want implement workflow", active)
	}
	if active.ParentRunID == nil || *active.ParentRunID != run.ID {
		t.Fatalf("child parent_run_id = %v want %q", active.ParentRunID, run.ID)
	}

	req = authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("advance past build status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "workflow_not_runnable") {
		t.Fatalf("body = %s", w.Body.String())
	}
}
