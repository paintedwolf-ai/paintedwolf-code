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

func TestWorkflowComposePolicyExtendsRequiredE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	manifest := `id: scratch
version: 1.0.0
phases:
  - id: only
    activity_label: Running
    complete_when: plan_stub_valid
`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var valErr workflowValidationFailure
	if err := json.Unmarshal(w.Body.Bytes(), &valErr); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	found := false
	for _, e := range valErr.Details.Errors {
		if e.Code == "extends_required" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %+v", valErr.Details.Errors)
	}
}

func TestWorkflowComposeFromTemplateE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	req := authedRequest(t, http.MethodGet, "/v1/workflow-templates", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("templates status = %d body = %s", w.Code, w.Body.String())
	}
	var listed wire.WorkflowTemplateListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	templates := listed.Templates
	if len(templates) < 3 {
		t.Fatalf("templates = %d want >= 3", len(templates))
	}

	body := `{"template_id":"hotfix-template","params":{"workflow_id":"e2e-hotfix"}}`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose-from-template", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("compose-from-template status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodPost, "/v1/sessions/{id}/workflows/compose-from-template", map[string]string{"id": sess.ID})
	var resp wire.ComposeWorkflowResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if resp.Summary.ID != "e2e-hotfix" || resp.Summary.Scope != wire.WorkflowScopeSession {
		t.Fatalf("summary = %+v", resp.Summary)
	}

	startBody := `{"workflow_id":"e2e-hotfix","workflow_version":"1.0.0"}`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start run status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestWorkflowComposePolicyVetSessionE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionWithPostureHTTP(t, srv, t.TempDir(), wire.SessionPostureVet)

	manifest := `id: vet-no-security
version: 1.0.0
extends: plan@1.0.0
initial_posture: vet
phases:
  - id: research
    activity_label: Understanding the request
    next: build
  - id: build
    activity_label: Building the change
    complete_when: plan_stub_valid
`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}
