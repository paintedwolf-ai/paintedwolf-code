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

func TestSessionWorkflowRunsListSurfacesParentRunIDOnChildInvoke(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	blueprintMgr := h.Workflows.Blueprints
	sess := createSessionHTTP(t, srv, t.TempDir())

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
		testutil.FailErr(t, "unmarshal start run", err)
	}
	run = advancePlanRunToExecuteHTTP(t, h, srv, blueprintMgr, sess, run)

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list runs status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}/workflow-runs", map[string]string{"id": sess.ID})

	var page wire.WorkflowRunPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		testutil.FailErr(t, "unmarshal listed runs", err)
	}
	listed := page.Runs
	var child *wire.WorkflowRun
	for i := range listed {
		if listed[i].WorkflowID == "implement" && listed[i].ParentRunID != nil {
			child = &listed[i]
			break
		}
	}
	if child == nil {
		t.Fatalf("listed runs missing implement child with parent_run_id: %+v", listed)
	}
	if *child.ParentRunID != run.ID {
		t.Fatalf("child parent_run_id = %q want %q", *child.ParentRunID, run.ID)
	}
}
