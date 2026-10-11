package hostcontracts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBoardRequiresProjectID(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := t.TempDir()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)

	delStore := delegation.NewMemoryStore()
	queue := worker.NewInMemoryQueue(2)
	snap := &board.SnapshotBuilder{Delegations: delStore, Workers: queue, Repo: repotest.NewProvider(t)}
	sessionStore := store.NewMemory()
	coord, err := sessionStore.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID, Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create coordinator session", err)
	// The board joins delegations by their canonical workspace path.
	_, err = delStore.Create(t.Context(), wire.Delegation{
		ProjectID: p.ID, WorkspacePath: project.PrimaryRootPath(p),
		Task:     "t",
		Strategy: wire.HuntStrategyFileBased,
	}, coord.ID, []wire.Leg{{Title: "leg"}})
	testutil.FailErr(t, "create session in store", err)

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionStore, Projects: reg}, Workflow: hostapi.WorkflowDependencies{
		Delegations: delegation.NewManager(delStore, queue, nil, nil), Workers: queue, Board: snap}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/board?session_id="+coord.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"delegation":[{`) {
		t.Fatalf("expected non-empty delegation roster, body = %s", w.Body.String())
	}
}

func TestBoardRequiresSessionIDMissing(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := t.TempDir()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store.NewMemory(), Projects: reg}, Workflow: hostapi.WorkflowDependencies{
		Delegations: delegation.NewManager(delegation.NewMemoryStore(), worker.NewInMemoryQueue(2), nil, nil),
		Workers:     worker.NewInMemoryQueue(2), Board: &board.SnapshotBuilder{Repo: repotest.NewProvider(t)}}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/board", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestWorkersRequiresSessionID(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := t.TempDir()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store.NewMemory(), Projects: reg}, Workflow: hostapi.WorkflowDependencies{
		Delegations: delegation.NewManager(delegation.NewMemoryStore(), worker.NewInMemoryQueue(2), nil, nil),
		Workers:     worker.NewInMemoryQueue(2)}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/workers?project_id="+p.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestCostSummaryRequiresSessionID(t *testing.T) {
	store := store.NewMemory()
	mock := llm.NewMockProvider(nil)
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	mgr := session.NewHost(store, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: tracker}, tools.NewStubRegistry())
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if err := tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID:        sess.ID,
		ProviderID:       "mock",
		Model:            "mock",
		PromptTokens:     10,
		CompletionTokens: 5,
	}); err != nil {
		t.Fatal(err)
	}

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}}), nil, hostapi.TestAPIToken)
	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/cost/summary?session_id="+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}
