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

func TestWorkflowPausedOnChildRejectsAdvance(t *testing.T) {
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
	run = advancePlanRunToExecuteHTTP(t, h, srv, blueprintMgr, sess, run)

	req = authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("execute advance status = %d body = %s", w.Code, w.Body.String())
	}
	var errBody wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if errBody.Code != "workflow_not_runnable" {
		t.Fatalf("code = %q", errBody.Code)
	}
}
