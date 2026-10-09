//go:build integration

package api

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	wire "github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPersistTestServer(t *testing.T) (*Server, wire.Session, workflowdrafts.Store) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	projectDir := t.TempDir()
	projReg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), projReg, projectDir)
	testutil.FailErr(t, "reg.Create failed", err)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create session in store", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, projectDir)
	sess, err = store.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(t.Context(), agents)
	sessionStore := workflowdrafts.NewMemory()
	policy, err := workflowcomposition.LoadComposePolicy()
	testutil.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	templates, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "load workflow templates", err)
	composer := &workflowcomposition.Composer{
		SessionStore: sessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
		Templates:    templates,
	}
	persister := &workflowcomposition.Persister{
		SessionStore: sessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
	}
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: projReg,
		WorkflowCatalog: workflowcatalog.Resolver{
			SessionStore:       sessionStore,
			ProjectTierApplies: func(context.Context, string) bool { return true },
		},
		WorkflowComposer: composer, WorkflowPersister: persister,
	}), nil, TestAPIToken)
	return srv, *sess, sessionStore
}

func TestPersistHandler201AndCatalog(t *testing.T) {
	srv, sess, sessionStore := newPersistTestServer(t)
	manifest := `id: hotfix-session
version: 1.0.0
extends: plan@1.0.0
initial_posture: spec
agents:
  - { id: plan-writer, tools: profile }
  - { id: implementer, tools: profile }
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: gates_satisfied
    gates:
      - delegation_closeout_complete
      - evidence_passed:verify
`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("compose status = %d body = %s", w.Code, w.Body.String())
	}

	body := `{"version":"1.0.0","confirm":true,"trigger":"/hotfix-session"}`
	req = newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/hotfix-session/persist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("persist status = %d body = %s", w.Code, w.Body.String())
	}
	var resp wire.PersistWorkflowResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if resp.Path != settingsoverlay.DirName()+"/workflows/hotfix-session/workflow.yaml" {
		t.Fatalf("path = %q", resp.Path)
	}
	if resp.Summary.Scope != wire.WorkflowScopeProject {
		t.Fatalf("scope = %q", resp.Summary.Scope)
	}
	if _, err := os.Stat(filepath.Join(sess.WorkspacePath, settingsoverlay.DirName(), "workflows", "hotfix-session", "workflow.yaml")); err != nil {
		testutil.FailErr(t, "stat path", err)
	}
	if _, err := sessionStore.Get(t.Context(), sess.ID, "hotfix-session", "1.0.0"); err == nil {
		t.Fatal("expected session row removed")
	}

	req = newAuthedRequest(http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d", w.Code)
	}
	var res wire.WorkflowListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	catalog := res.Workflows
	found := false
	for _, item := range catalog {
		if item.ID == "hotfix-session" && item.Scope == wire.WorkflowScopeProject {
			found = true
		}
		if item.ID == "hotfix-session" && item.Scope == wire.WorkflowScopeSession {
			t.Fatal("session scope should be gone")
		}
	}
	if !found {
		t.Fatalf("catalog missing project hotfix-session: %+v", catalog)
	}
}

func TestPersistHandler403WithoutConfirm(t *testing.T) {
	srv, sess, sessionStore := newPersistTestServer(t)
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
	if err := sessionStore.Upsert(t.Context(), sess.ID, []byte(manifest), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "sessionStore.Upsert failed", err)
	}
	body := `{"version":"1.0.0","confirm":false}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/hotfix-session/persist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestPersistHandlerRejectsInvalidJSON(t *testing.T) {
	srv, sess, _ := newPersistTestServer(t)
	req := newAuthedRequest(
		http.MethodPost,
		"/v1/sessions/"+sess.ID+"/workflows/hotfix-session/persist",
		strings.NewReader(`{`),
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusBadRequest, "invalid_json")
}
