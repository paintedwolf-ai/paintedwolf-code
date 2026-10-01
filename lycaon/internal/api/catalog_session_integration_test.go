//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestListWorkflowsIncludesSessionScope(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	projReg := project.NewMemoryRegistry()
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create session in store", err)

	sessionStore := workflow.NewMemorySessionWorkflowStore()
	manifestYAML := `id: hotfix-session
version: 1.0.0
request:
  question: What should this workflow do?
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	if err := sessionStore.Upsert(t.Context(), sess.ID, []byte(manifestYAML), workflow.ComposeActorCoordinator, nil); err != nil {
		testutil.FailErr(t, "sessionStore.Upsert failed", err)
	}

	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: projReg,
		WorkflowCatalog: workflow.ManifestResolver{SessionStore: sessionStore},
	}), nil, TestAPIToken)

	req := newAuthedRequest(http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var list wire.WorkflowListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	summaries := list.Workflows
	var hotfix *wire.WorkflowSummary
	for i := range summaries {
		if summaries[i].ID == "hotfix-session" {
			hotfix = &summaries[i]
			break
		}
	}
	if hotfix == nil {
		t.Fatal("hotfix-session missing from catalog")
	}
	if hotfix.Scope != wire.WorkflowScopeSession {
		t.Fatalf("scope = %q want session", hotfix.Scope)
	}
}

func TestListWorkflowsSessionNotFound(t *testing.T) {
	store := store.NewMemory()
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: project.NewMemoryRegistry(), WorkflowCatalog: workflow.ManifestResolver{},
	}), nil, TestAPIToken)

	req := newAuthedRequest(http.MethodGet, "/v1/workflows?session_id=missing-session", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}
