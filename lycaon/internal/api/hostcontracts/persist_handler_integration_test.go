package hostcontracts

import (
	"encoding/json"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	wire "github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistHandler201AndCatalog(t *testing.T) {
	srv, sess, sessionStore := contractfixture.NewPersistTestServer(t)
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
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("compose status = %d body = %s", w.Code, w.Body.String())
	}

	body := `{"version":"1.0.0","confirm":true,"trigger":"/hotfix-session"}`
	req = contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/hotfix-session/persist", strings.NewReader(body))
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

	req = contractfixture.NewAuthedRequest(http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
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
	srv, sess, sessionStore := contractfixture.NewPersistTestServer(t)
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
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/hotfix-session/persist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestPersistHandlerRejectsInvalidJSON(t *testing.T) {
	srv, sess, _ := contractfixture.NewPersistTestServer(t)
	req := contractfixture.NewAuthedRequest(
		http.MethodPost,
		"/v1/sessions/"+sess.ID+"/workflows/hotfix-session/persist",
		strings.NewReader(`{`),
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	contractfixture.AssertErrorResponse(t, w, http.StatusBadRequest, "invalid_json")
}
