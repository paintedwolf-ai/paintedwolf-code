package workflowadmin_test

import (
	"context"
	"encoding/json"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sessionadmin"
	"github.com/lycaon/lycaon/internal/api/sessionview"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/api/workflowadmin"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// routesFixture mounts the workflow handlers over durable stores sharing one
// disposable database.
type routesFixture struct {
	router     chi.Router
	handler    workflowadmin.Handler
	sessions   *store.SQL
	runs       *runstate.Repository
	blueprints *blueprint.Manager
	project    *project.Project
	workDir    string
}

func newRoutesFixture(t *testing.T, orchestrator orchestration.Orchestrator) *routesFixture {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "workflowadmin.db")
	sessions := store.NewSQL(sqlDB)
	projects := project.NewSQLRegistry(sqlDB)
	workDir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), projects, workDir)
	testutil.FailErr(t, "create project", err)

	deps := apitest.Dependencies(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessions, Projects: projects}})
	deps.Workflow.Workflows.Blueprints.Getter = deps.Workflow.Blueprints
	deps.Workflow.Workflows.Presentation.BlueprintGetter = deps.Workflow.Blueprints
	view := sessionview.New(sessionview.Projector{
		Workflows: deps.Workflow.Workflows, Store: sessions, Sessions: deps.Core.Sessions, Projects: projects,
	})
	h := workflowadmin.New(&httpio.Responder{Logger: slog.New(slog.DiscardHandler)}, &taskgroup.Group{}, workflowadmin.Deps{
		Workflows: deps.Workflow.Workflows, Runs: deps.Workflow.WorkflowRuns,
		Composer: deps.Workflow.WorkflowComposer, Persister: deps.Workflow.WorkflowPersister, Blueprints: deps.Workflow.Blueprints,
		Orchestrator: orchestrator, EventPublisher: deps.Host.EventPublisher, ManagedSecrets: deps.Approvals.ManagedSecrets,
		Projects: projects, Store: sessions, Sessions: deps.Core.Sessions,
		SessionAdmin: &sessionadmin.Handler{}, SessionView: &view,
	})
	return &routesFixture{
		router: workflowRouter(h), handler: h, sessions: sessions, runs: deps.Workflow.WorkflowRuns,
		blueprints: deps.Workflow.Blueprints, project: p, workDir: workDir,
	}
}

// workflowRouter mirrors the production route patterns for these handlers.
func workflowRouter(h workflowadmin.Handler) chi.Router {
	r := chi.NewRouter()
	rc, comp, bp := h.RunControl, h.Composition, h.BlueprintRoutes
	r.Get("/v1/workflows", rc.HandleListWorkflows)
	r.Get("/v1/workflow-templates", comp.HandleListWorkflowTemplates)
	r.Post("/v1/sessions/{id}/workflows/compose", comp.HandleComposeWorkflow)
	r.Post("/v1/sessions/{id}/workflows/compose-from-template", comp.HandleComposeFromTemplate)
	r.Post("/v1/sessions/{id}/workflows/{workflow_id}/persist", comp.HandlePersistWorkflow)
	r.Post("/v1/sessions/{id}/workflow-runs", rc.HandleStartWorkflowRun)
	r.Get("/v1/sessions/{id}/workflow-runs", rc.HandleListSessionWorkflowRuns)
	r.Get("/v1/sessions/{id}/workflow-runs/active", rc.HandleGetActiveWorkflowRun)
	r.Get("/v1/workflow-runs/{id}", rc.HandleGetWorkflowRun)
	r.Get("/v1/workflow-runs/{id}/report", h.Reports.HandleGetWorkflowRunReport)
	r.Post("/v1/workflow-runs/{id}/exit", rc.HandleExitWorkflowRun)
	r.Post("/v1/workflow-runs/{id}/pause", rc.HandlePauseWorkflowRun)
	r.Post("/v1/workflow-runs/{id}/resume", rc.HandleResumeWorkflowRun)
	r.Post("/v1/workflow-runs/{id}/cancel", rc.HandleCancelWorkflowRun)
	r.Post("/v1/workflow-runs/{id}/advance", rc.HandleAdvanceWorkflowRun)
	r.Post("/v1/workflow-runs/{id}/transitions/{transition_id}", rc.HandleFireWorkflowTransition)
	r.Post("/v1/workflow-runs/{id}/decisions/{phase_id}", rc.HandleResolveWorkflowDecision)
	r.Post("/v1/workflow-runs/{id}/feedback/{phase_id}", rc.HandleResolveWorkflowFeedback)
	r.Post("/v1/workflow-runs/{id}/feedback/{phase_id}/secret", rc.HandleResolveWorkflowSecret)
	r.Post("/v1/projects/{id}/blueprints/{blueprint_id}/approve", bp.HandleApproveBlueprint)
	r.Post("/v1/projects/{id}/blueprints/{blueprint_id}/launch", bp.HandleLaunchBlueprint)
	return r
}

func (f *routesFixture) createSession(t *testing.T) *wire.Session {
	t.Helper()
	sess, err := f.sessions.Create(t.Context(), wire.CreateSessionRequest{
		ProjectID: f.project.ID, Posture: wire.SessionPostureBuild,
	}, f.project.ID)
	testutil.FailErr(t, "create session", err)
	return sess
}

// seedRun stores a running run for an uncataloged workflow in a new session.
func (f *routesFixture) seedRun(t *testing.T, blueprintPath string) *wire.WorkflowRun {
	t.Helper()
	sess := f.createSession(t)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	run := &wire.WorkflowRun{
		ID: uuid.NewString(), SessionID: sess.ID, ProjectID: f.project.ID,
		WorkflowID: "uncataloged-workflow", WorkflowVersion: "1.0.0",
		Status: wire.WorkflowRunStatusRunning, CurrentPhase: "build", BlueprintPath: blueprintPath,
		CreatedAt: at, UpdatedAt: at,
	}
	testutil.FailErr(t, "create run", f.runs.CreateState(t.Context(), run, f.workDir, nil))
	stored, err := f.runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "reload run", err)
	return stored
}

func (f *routesFixture) do(t *testing.T, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *routesFixture) postJSON(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, http.MethodPost, path, httpio.MediaTypeJSON, body)
}

// wantError asserts the structured code and its declared status.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, code wire.ApiErrorCode) {
	t.Helper()
	if rec.Code != code.HTTPStatus() {
		t.Fatalf("status = %d want %d (%s); body = %s", rec.Code, code.HTTPStatus(), code, rec.Body.String())
	}
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode error response", json.Unmarshal(rec.Body.Bytes(), &resp))
	if resp.Code != code {
		t.Fatalf("code = %q want %q; body = %s", resp.Code, code, rec.Body.String())
	}
}

func decodeOK[T any](t *testing.T, rec *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d want %d; body = %s", rec.Code, status, rec.Body.String())
	}
	var out T
	testutil.FailErr(t, "decode response", json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

// recordingOrchestrator records settlement requests and never runs a topology.
type recordingOrchestrator struct {
	mu   sync.Mutex
	runs []orchestration.RunRequest
}

func (o *recordingOrchestrator) LoadTopology(context.Context, extpacks.Source) (*orchestration.TopologySpec, error) {
	return nil, nil
}

func (o *recordingOrchestrator) Run(_ context.Context, req orchestration.RunRequest) (*orchestration.RunResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.runs = append(o.runs, req)
	return &orchestration.RunResult{}, nil
}

func (o *recordingOrchestrator) Status(context.Context, string) (*orchestration.RunStatus, error) {
	return nil, nil
}

func (o *recordingOrchestrator) Cancel(context.Context, string, orchestration.TerminationReason) error {
	return nil
}

func (o *recordingOrchestrator) started() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.runs)
}
