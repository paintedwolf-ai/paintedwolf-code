package security

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStartPlanHTTPMatchesOpenAPI(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(planStartBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodPost, "/v1/sessions/{id}/workflow-runs", map[string]string{"id": sess.ID})
}

func TestCoordinatorProposesRunBeforeHumanStart(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	toolReg := h.ToolRegistry
	ctx := context.Background()

	_, err := toolReg.Run(ctx, "state_start", map[string]any{
		"workflow_id": "plan", "workflow_version": "1.0.0",
	}, securityToolContext(sess.ID, sess.WorkspacePath, "coordinator"))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKFLOW_START_REQUIRES_HUMAN_APPROVAL" {
		t.Fatalf("state_start without approval err = %v, want WORKFLOW_START_REQUIRES_HUMAN_APPROVAL reject", err)
	}

	run, err := h.WorkflowMgr.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.WorkflowID != "plan" {
		t.Fatalf("workflow_id = %q", run.WorkflowID)
	}

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/coordinator-context", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("context status = %d", w.Code)
	}
	var runCtx wire.CoordinatorRunContext
	if err := json.Unmarshal(w.Body.Bytes(), &runCtx); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if runCtx.WorkflowID != "plan" {
		t.Fatalf("workflow_id = %q", runCtx.WorkflowID)
	}
}

func TestWorkflowComposeE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())

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
	var composeResp wire.ComposeWorkflowResponse
	if err := json.Unmarshal(w.Body.Bytes(), &composeResp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if composeResp.Summary.Scope != wire.WorkflowScopeSession {
		t.Fatalf("scope = %q", composeResp.Summary.Scope)
	}
	if strings.TrimSpace(composeResp.EffectiveSummary.CoordinatorBrief) == "" {
		t.Fatal("expected coordinator_brief")
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
		t.Fatalf("catalog missing hotfix-session: %+v", catalog)
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
	if run.WorkflowID != "hotfix-session" {
		t.Fatalf("workflow_id = %q", run.WorkflowID)
	}

	revised := string(manifestYAML)
	revised = strings.Replace(revised, "initial_posture: spec", "initial_posture: spec\n# revised", 1)
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(revised))
	req.Header.Set("Content-Type", "application/yaml")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("second compose status = %d body = %s", w.Code, w.Body.String())
	}

	badAgent := `id: bad-agent-compose
version: 1.0.0
agents:
  - { id: totally-fake-agent-id, tools: profile }
phases:
  - id: only
    activity_label: Running
    complete_when: plan_stub_valid
`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(badAgent))
	req.Header.Set("Content-Type", "application/yaml")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid agent status = %d body = %s", w.Code, w.Body.String())
	}
	var valErr workflowValidationFailure
	if err := json.Unmarshal(w.Body.Bytes(), &valErr); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(valErr.Details.Errors) == 0 || valErr.Details.Errors[0].Code != "unknown_agent" {
		t.Fatalf("errors = %+v", valErr.Details.Errors)
	}
}

func TestWorkflowComposeDryRunE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	manifest := `id: dry-run-only
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
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose?dry_run=true", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dry_run compose status = %d body = %s", w.Code, w.Body.String())
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
	for _, item := range catalog {
		if item.ID == "dry-run-only" {
			t.Fatalf("dry_run stored workflow in catalog: %+v", catalog)
		}
	}
}
