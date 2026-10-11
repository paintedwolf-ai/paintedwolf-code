//go:build integration

package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
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

	sessionStore := workflowdrafts.NewMemory()
	manifestYAML := `id: hotfix-session
version: 1.0.0
request:
  question: What should this workflow do?
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	if err := sessionStore.Upsert(t.Context(), sess.ID, []byte(manifestYAML), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "sessionStore.Upsert failed", err)
	}

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: projReg}, Workflow: hostapi.WorkflowDependencies{
		WorkflowCatalog: workflowcatalog.Resolver{SessionStore: sessionStore}}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
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
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: project.NewMemoryRegistry()}, Workflow: hostapi.WorkflowDependencies{WorkflowCatalog: workflowcatalog.Resolver{}}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/workflows?session_id=missing-session", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}
