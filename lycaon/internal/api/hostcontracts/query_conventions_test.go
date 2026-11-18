package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"github.com/lycaon/lycaon/internal/worker/workertest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBoardRequiresProjectID(t *testing.T) {
	sessionFixture := storetest.New(t)

	dir := t.TempDir()
	reg := project.NewSQLRegistry(sessionFixture.DB())
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)

	sessionStore := sessionFixture
	coord, err := sessionStore.Sessions.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID, Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create coordinator session", err)
	queue := workertest.OnDatabase(t, sessionFixture.DB(), 2)
	snap := &board.SnapshotBuilder{Workers: queue, Repo: repotest.NewProvider(t)}
	_, err = queue.Enqueue(t.Context(), wire.WorkerTask{
		ID:              "w-board-1",
		ParentSessionID: coord.ID,
		ProjectID:       p.ID,
		WorkspacePath:   project.PrimaryRootPath(p),
		AgentType:       "implementer",
		Prompt:          "implement feature",
		Brief:           "implement feature",
		Status:          wire.WorkerStatusRunning,
	})
	testutil.FailErr(t, "enqueue worker", err)

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionStore, Projects: reg}, Workflow: hostapi.WorkflowDependencies{
		Workers: queue, Board: snap}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/board?session_id="+coord.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var view wire.BoardView
	testutil.FailErr(t, "decode board", json.Unmarshal(w.Body.Bytes(), &view))
	if view.SessionID != coord.ID {
		t.Fatalf("board is for session %q, want %q", view.SessionID, coord.ID)
	}
}

func TestBoardRequiresSessionIDMissing(t *testing.T) {
	sessionFixture := storetest.New(t)

	dir := t.TempDir()
	reg := project.NewSQLRegistry(sessionFixture.DB())
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionFixture, Projects: reg}, Workflow: hostapi.WorkflowDependencies{
		Workers: workertest.OnDatabase(t, sessionFixture.DB(), 2), Board: &board.SnapshotBuilder{Repo: repotest.NewProvider(t)}}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/board", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestWorkersRequiresSessionID(t *testing.T) {
	sessionFixture := storetest.New(t)

	dir := t.TempDir()
	reg := project.NewSQLRegistry(sessionFixture.DB())
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionFixture, Projects: reg}, Workflow: hostapi.WorkflowDependencies{
		Workers: workertest.OnDatabase(t, sessionFixture.DB(), 2)}}), nil, hostapi.TestAPIToken)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/workers?project_id="+p.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestCostSummaryRequiresSessionID(t *testing.T) {
	sessionFixture := storetest.New(t, testdbseed.DefaultProjectID)

	store := sessionFixture
	mock := llm.NewMockProvider(nil)
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	mgr := session.NewHost(store, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: tracker}, toolstest.NewRegistry())
	sess, err := store.Sessions.Create(t.Context(), wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if err := tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID:        sess.ID,
		ProviderID:       "mock",
		Model:            "mock",
		PromptTokens:     10,
		CompletionTokens: 5,
	}); err != nil {
		testutil.FailErr(t, "tracker.RecordUsage failed", err)
	}

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store, Projects: project.NewSQLRegistry(sessionFixture.DB()), Sessions: mgr}}), nil, hostapi.TestAPIToken)
	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/cost/summary?session_id="+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}
