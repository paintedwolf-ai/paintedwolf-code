//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func newComposeTestServer(t *testing.T) (*Server, wire.Session, *workflow.Composer) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	projReg := project.NewMemoryRegistry()
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create session in store", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(t.Context(), agents)
	sessionStore := workflow.NewMemorySessionWorkflowStore()
	policy, err := workflow.LoadComposePolicy()
	testutil.FailErr(t, "workflow.LoadComposePolicy failed", err)
	templates, err := workflow.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "load workflow templates", err)
	composer := &workflow.Composer{
		SessionStore: sessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
		Templates:    templates,
	}
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: projReg,
		WorkflowCatalog:  workflow.ManifestResolver{SessionStore: sessionStore},
		WorkflowComposer: composer,
	}), nil, TestAPIToken)
	return srv, *sess, composer
}

func TestComposeHandler201AndCatalog(t *testing.T) {
	srv, sess, composer := newComposeTestServer(t)
	manifest := `id: hotfix-session
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: delegation_closeout_complete
`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/yaml")
	req.Header.Set("X-Lycaon-Actor", "forged")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("compose status = %d body = %s", w.Code, w.Body.String())
	}
	var resp wire.ComposeWorkflowResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if resp.Summary.Scope != wire.WorkflowScopeSession {
		t.Fatalf("scope = %q", resp.Summary.Scope)
	}
	rows, err := composer.SessionStore.ListBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "composer.SessionStore.ListBySession failed", err)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].CreatedBy != workflow.ComposeActorUser {
		t.Fatalf("created_by = %q, want user", rows[0].CreatedBy)
	}

	req = newAuthedRequest(http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d", w.Code)
	}
}

func TestComposeHandlerRejectsMislabeledYAML(t *testing.T) {
	srv, sess, composer := newComposeTestServer(t)
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader("id: mislabeled"))
	req.Header.Set("Content-Type", httpio.MediaTypeJSON)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusUnsupportedMediaType, "unsupported_media_type")
	rows, err := composer.SessionStore.ListBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "list composed workflows", err)
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want no mutation", len(rows))
	}
}

func TestComposeHandlerRejectsMalformedDryRunWithoutMutation(t *testing.T) {
	srv, sess, composer := newComposeTestServer(t)
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose?dry_run=1", strings.NewReader("id: malformed-query"))
	req.Header.Set("Content-Type", httpio.MediaTypeYAML)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusBadRequest, "invalid_query")
	rows, err := composer.SessionStore.ListBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "list composed workflows", err)
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want no mutation", len(rows))
	}
}

func TestComposeHandlerDryRunNoRow(t *testing.T) {
	srv, sess, composer := newComposeTestServer(t)
	manifest := `id: dry-only
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: delegation_closeout_complete
`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose?dry_run=true", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	rows, err := composer.SessionStore.ListBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "composer.SessionStore.ListBySession failed", err)
	if len(rows) != 0 {
		t.Fatalf("rows = %d want 0", len(rows))
	}
}

func TestComposeHandler422Shape(t *testing.T) {
	srv, sess, _ := newComposeTestServer(t)
	manifest := `id: bad
version: 1.0.0
trigger: /nope
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code    wire.ApiErrorCode             `json:"code"`
		Details wire.ComposeValidationDetails `json:"details"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if resp.Code != wire.ApiErrorCodeWorkflowValidationFailed || len(resp.Details.Errors) == 0 {
		t.Fatalf("expected workflow_validation_failed with errors, got %+v", resp)
	}
}

func TestComposeFromTemplateRejectsInvalidJSON(t *testing.T) {
	srv, sess, _ := newComposeTestServer(t)
	req := newAuthedRequest(
		http.MethodPost,
		"/v1/sessions/"+sess.ID+"/workflows/compose-from-template",
		strings.NewReader(`{`),
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusBadRequest, "invalid_json")
}
