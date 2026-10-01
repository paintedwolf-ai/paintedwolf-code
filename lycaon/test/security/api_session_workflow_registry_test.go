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

func TestSessionWorkflowRegistryE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	sessionStore := h.SessionWorkflowStore
	sqlDB := h.DB

	manifestYAML := `id: hotfix-session
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Understanding the request
    next: build
  - id: build
    activity_label: Building the change
    invoke_workflow:
      workflow_id: implement
      version: "1.0.0"
      blueprint: none
    complete_when: gates_satisfied
    gates: [child_run_complete]
`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(manifestYAML))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("compose status = %d body = %s", w.Code, w.Body.String())
	}

	req = authedRequest(t, http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body = %s", w.Code, w.Body.String())
	}
	var listed wire.WorkflowListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	catalog := listed.Workflows
	found := false
	for _, item := range catalog {
		if item.ID == "hotfix-session" && item.Scope == wire.WorkflowScopeSession {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("catalog missing session-scoped hotfix-session: %+v", catalog)
	}

	startBody := `{"workflow_id":"hotfix-session","workflow_version":"1.0.0"}`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start run status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if run.WorkflowID != "hotfix-session" || run.CurrentPhase != "research" {
		t.Fatalf("run = %+v", run)
	}

	if _, err := sqlDB.ExecContext(t.Context(), `DELETE FROM messages WHERE session_id = ?`, sess.ID); err != nil {
		testutil.FailErr(t, "sqlDB.Exec failed", err)
	}
	if _, err := sqlDB.ExecContext(t.Context(), `DELETE FROM workflow_runs WHERE session_id = ?`, sess.ID); err != nil {
		testutil.FailErr(t, "sqlDB.Exec failed", err)
	}
	if _, err := sqlDB.ExecContext(t.Context(), `DELETE FROM sessions WHERE id = ?`, sess.ID); err != nil {
		testutil.FailErr(t, "sqlDB.Exec failed", err)
	}
	rows, err := sessionStore.ListBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "sessionStore.ListBySession failed", err)
	if len(rows) != 0 {
		t.Fatalf("session_workflows rows after cascade = %d", len(rows))
	}

	req = authedRequest(t, http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("catalog after delete status = %d body = %s", w.Code, w.Body.String())
	}
}
