package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkflowPersistE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	proj := createProjectHTTP(t, srv, t.TempDir())
	sess := createSessionForProjectHTTP(t, srv, proj.ID, wire.SessionPostureBuild)

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	manifestPath := filepath.Join(filepath.Dir(file), "..", "contract", "testdata", "workflow", "compose_session_plan.yaml")
	manifestYAML, err := os.ReadFile(manifestPath)
	testutil.FailErr(t, "read file", err)

	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(string(manifestYAML)))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("compose status = %d body = %s", w.Code, w.Body.String())
	}

	body := `{"version":"1.0.0","confirm":false}`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/hotfix-session/persist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("persist without confirm status = %d body = %s", w.Code, w.Body.String())
	}

	body = `{"version":"1.0.0","confirm":true,"trigger":"/hotfix-session-custom"}`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/hotfix-session/persist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("persist status = %d body = %s", w.Code, w.Body.String())
	}
	var persistResp wire.PersistWorkflowResponse
	if err := json.Unmarshal(w.Body.Bytes(), &persistResp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if persistResp.Summary.Scope != wire.WorkflowScopeProject {
		t.Fatalf("scope = %q", persistResp.Summary.Scope)
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
	foundProject := false
	for _, item := range catalog {
		if item.ID == "hotfix-session" && item.Scope == wire.WorkflowScopeProject {
			foundProject = true
		}
	}
	if !foundProject {
		t.Fatalf("catalog missing persisted workflow: %+v", catalog)
	}

	startBody := `{"workflow_id":"hotfix-session","workflow_version":"1.0.0"}`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start run status = %d body = %s", w.Code, w.Body.String())
	}

}

// An omitted trigger inherits from the manifest and participates in collision checks.
func TestWorkflowPersistMissingTriggerInheritsAndCollides(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	sessionWFStore := h.SessionWorkflowStore
	manifest := `id: hotfix
version: 1.0.0
extends: plan@1.0.0
initial_posture: spec
agents:
  - { id: plan-writer, tools: profile }
  - { id: implementer, tools: profile }
phases:
  - id: research
    activity_label: Understanding the request
    next: build
  - id: build
    activity_label: Building the change
    invoke_workflow:
      workflow_id: implement
      version: "1.0.0"
      blueprint: inherit
    complete_when: gates_satisfied
    gates:
      - child_run_complete
      - evidence_passed:verify
`
	if err := sessionWFStore.Upsert(t.Context(), sess.ID, []byte(manifest), workflow.ComposeActorCoordinator, nil); err != nil {
		testutil.FailErr(t, "sessionWFStore.Upsert failed", err)
	}
	body := `{"version":"1.0.0","confirm":true}`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/hotfix/persist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp workflowValidationFailure
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(resp.Details.Errors) == 0 || resp.Details.Errors[0].Code != "trigger_collision" {
		t.Fatalf("expected trigger_collision, got errors = %+v", resp.Details.Errors)
	}
}

func TestWorkflowPersistDuplicateTrigger422(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	sessionWFStore := h.SessionWorkflowStore
	manifest := `id: hotfix
version: 1.0.0
extends: plan@1.0.0
initial_posture: spec
agents:
  - { id: plan-writer, tools: profile }
  - { id: implementer, tools: profile }
phases:
  - id: research
    activity_label: Understanding the request
    next: build
  - id: build
    activity_label: Building the change
    invoke_workflow:
      workflow_id: implement
      version: "1.0.0"
      blueprint: inherit
    complete_when: gates_satisfied
    gates:
      - child_run_complete
      - evidence_passed:verify
`
	if err := sessionWFStore.Upsert(t.Context(), sess.ID, []byte(manifest), workflow.ComposeActorCoordinator, nil); err != nil {
		testutil.FailErr(t, "sessionWFStore.Upsert failed", err)
	}
	body := `{"version":"1.0.0","confirm":true,"trigger":"/plan"}`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/hotfix/persist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp workflowValidationFailure
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(resp.Details.Errors) == 0 || resp.Details.Errors[0].Code != "trigger_collision" {
		t.Fatalf("errors = %+v", resp.Details.Errors)
	}
}

// Persisting a workflow the session never composed maps to a typed 404.
func TestWorkflowPersistUnknownWorkflowNotFound(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	proj := createProjectHTTP(t, srv, t.TempDir())
	sess := createSessionForProjectHTTP(t, srv, proj.ID, wire.SessionPostureBuild)

	body := `{"version":"1.0.0","confirm":true,"trigger":"/never-composed"}`
	req := authedRequest(t, http.MethodPost,
		"/v1/sessions/"+sess.ID+"/workflows/never-composed/persist", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404; body = %s", w.Code, w.Body.String())
	}
	var errResp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		testutil.FailErr(t, "decode error response", err)
	}
	if string(errResp.Code) != "session_workflow_not_found" {
		t.Fatalf("code = %q want session_workflow_not_found; body = %s", errResp.Code, w.Body.String())
	}
}
