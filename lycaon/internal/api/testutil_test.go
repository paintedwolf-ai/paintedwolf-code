package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func newAuthedRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", httpio.MediaTypeJSON)
	}
	if strings.HasPrefix(target, "/v1") {
		WithTestAuth(req)
	}
	return req
}

// testDeps adjusts the dependencies a test server is built with.
type testDeps func(*Dependencies)

func newTestServer(t *testing.T, opts ...testDeps) *Server {
	t.Helper()
	return newTestServerWithRegistry(t, tools.NewStubRegistry(), opts...)
}

func newTestServerWithRegistry(t *testing.T, reg tools.ToolRegistry, opts ...testDeps) *Server {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewHost(store, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, reg)
	mgr.SetDataDir(t.TempDir())
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: reg})
	// Stub bindings do not expose coordinator tools.
	wireTestBindingRegistry(t)
	return newServerForTest(t, Dependencies{Core: CoreDependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}}, opts...)
}

// newServerForTest builds a server with the bundled user notices after opts
// adjust deps, and stops its background work when the test ends.
func newServerForTest(t *testing.T, deps Dependencies, opts ...testDeps) *Server {
	t.Helper()
	deps.Core.UserNotices = testUserNotices(t)
	for _, opt := range opts {
		opt(&deps)
	}
	srv := NewServer(requiredTestDeps(t, deps), nil, TestAPIToken)
	stopBackgroundOnCleanup(t, srv)
	return srv
}

// requiredTestDeps supplies every dependency the route families require that
// deps leaves unset.
func requiredTestDeps(t *testing.T, deps Dependencies) Dependencies {
	t.Helper()
	fill := apitestdeps.Deps{
		ApprovalGate:      deps.Approvals.ApprovalGate,
		ApprovalDecisions: deps.Approvals.Authority.ApprovalDecisions,
		Database:          deps.Core.Database, Store: deps.Core.Store, Projects: deps.Core.Projects, Sessions: deps.Core.Sessions,
		Settings: deps.Core.Settings, Invocations: deps.Core.Invocations, MutationGate: deps.Core.MutationGate,
		ManagedSecrets: deps.Approvals.ManagedSecrets, SecretIgnores: deps.Approvals.SecretIgnores, SourceLedger: deps.Source.SourceLedger,
		SourceMutations: deps.Source.SourceMutations, FileOperations: deps.Source.FileOperations,
		EditorDocuments: deps.Source.EditorDocuments, FileBriefings: deps.Source.FileBriefings, Workflows: deps.Workflow.Workflows,
		WorkflowRuns: deps.Workflow.WorkflowRuns, WorkflowComposer: deps.Workflow.WorkflowComposer, WorkflowPersister: deps.Workflow.WorkflowPersister,
		Blueprints: deps.Workflow.Blueprints, ScanCoordinator: deps.Scans.ScanCoordinator, ScanCadence: deps.Scans.ScanCadence,
		PublishDetections: deps.Scans.PublishDetections, DataDir: deps.Storage.DataDir, ModuleRoot: deps.Storage.ModuleRoot,
		MCP: deps.External.MCP, ExtensionViews: deps.Extensions.ExtensionViews,
		ContributionReceipts: deps.Extensions.Contributions.Receipts, ContributionAuthority: deps.Extensions.Contributions.Authority,
		LLM: deps.Providers.LLM, CostTracker: deps.Providers.CostTracker, Events: deps.Host.Events, EventPublisher: deps.Host.EventPublisher,
		HostIdentity: deps.Host.HostIdentity, Checkpoints: deps.Approvals.Checkpoints, ProgressStore: deps.Source.ProgressStore,
		VisualStore: deps.Source.VisualStore, HistoryStorage: deps.External.HistoryStorage, HostResources: deps.Host.HostResources,
		HostPower: deps.Host.HostPower, Pricing: deps.Host.Pricing, AgentPresence: deps.Source.AgentPresence, Workers: deps.Workflow.Workers,
		WorkerCancel: deps.Workflow.WorkerCancel, Delegations: deps.Workflow.Delegations, Board: deps.Workflow.Board,
		HarnessWorkers: deps.Harness.HarnessWorkers, WebResearch: deps.External.WebResearch, WebDiscoverer: deps.External.WebDiscoverer,
	}
	apitestdeps.Fill(t, &fill)
	deps.Approvals.ApprovalGate = fill.ApprovalGate
	deps.Approvals.Authority.ApprovalDecisions = fill.ApprovalDecisions
	deps.Core.Database, deps.Core.Store, deps.Core.Projects, deps.Core.Sessions = fill.Database, fill.Store, fill.Projects, fill.Sessions
	deps.Core.Settings, deps.Core.Invocations, deps.Core.MutationGate = fill.Settings, fill.Invocations, fill.MutationGate
	deps.Approvals.ManagedSecrets, deps.Approvals.SecretIgnores, deps.Source.SourceLedger = fill.ManagedSecrets, fill.SecretIgnores, fill.SourceLedger
	deps.Source.SourceMutations, deps.Source.FileOperations = fill.SourceMutations, fill.FileOperations
	deps.Source.EditorDocuments, deps.Source.FileBriefings, deps.Workflow.Workflows = fill.EditorDocuments, fill.FileBriefings, fill.Workflows
	deps.Workflow.WorkflowRuns, deps.Workflow.WorkflowComposer, deps.Workflow.WorkflowPersister = fill.WorkflowRuns, fill.WorkflowComposer, fill.WorkflowPersister
	deps.Workflow.Blueprints, deps.Scans.ScanCoordinator, deps.Scans.ScanCadence = fill.Blueprints, fill.ScanCoordinator, fill.ScanCadence
	deps.Scans.PublishDetections, deps.Storage.DataDir, deps.Storage.ModuleRoot = fill.PublishDetections, fill.DataDir, fill.ModuleRoot
	deps.External.MCP, deps.Extensions.ExtensionViews = fill.MCP, fill.ExtensionViews
	deps.Extensions.Contributions.Receipts, deps.Extensions.Contributions.Authority = fill.ContributionReceipts, fill.ContributionAuthority
	deps.Providers.LLM, deps.Providers.CostTracker, deps.Host.Events, deps.Host.EventPublisher = fill.LLM, fill.CostTracker, fill.Events, fill.EventPublisher
	deps.Host.HostIdentity, deps.Approvals.Checkpoints, deps.Source.ProgressStore = fill.HostIdentity, fill.Checkpoints, fill.ProgressStore
	deps.Source.VisualStore, deps.External.HistoryStorage, deps.Host.HostResources = fill.VisualStore, fill.HistoryStorage, fill.HostResources
	deps.Host.HostPower, deps.Host.Pricing, deps.Source.AgentPresence = fill.HostPower, fill.Pricing, fill.AgentPresence
	deps.Workflow.Workers, deps.Workflow.WorkerCancel, deps.Workflow.Delegations, deps.Workflow.Board = fill.Workers, fill.WorkerCancel, fill.Delegations, fill.Board
	deps.Harness.HarnessWorkers = fill.HarnessWorkers
	deps.External.WebResearch, deps.External.WebDiscoverer = fill.WebResearch, fill.WebDiscoverer
	return deps
}

// wireTestBindingRegistry isolates tests that replace the process-wide registry.
func wireTestBindingRegistry(t *testing.T) {
	t.Helper()
	reg, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		t.Fatalf("load Binding registry: %v", err)
	}
	previous := anchor.DefaultRegistry()
	anchor.SetDefaultRegistry(reg)
	t.Cleanup(func() { anchor.SetDefaultRegistry(previous) })
}

// Stop recurring work before fixture storage closes.
func stopBackgroundOnCleanup(t *testing.T, srv *Server) {
	t.Helper()
	t.Cleanup(func() {
		http.DefaultClient.CloseIdleConnections()
		srv.StopBackground()
		drainBackground(t, srv)
		if _, released := sourcesReleased.LoadOrStore(srv, struct{}{}); !released {
			releaseProjectSources(t, srv)
		}
	})
}

// sourcesReleased lets only a server's first teardown release its sources,
// while every registry it was given is still open.
var sourcesReleased sync.Map

// releaseProjectSources releases the process-wide source watches and catalog
// trees as project deletion does; left bound, they write under removed TempDirs
// and keep retrying inventory for the rest of the test binary.
func releaseProjectSources(t *testing.T, srv *Server) {
	t.Helper()
	if srv.Sources.Workspace.ProjectRegistry == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	projects, err := srv.Sources.Workspace.ProjectRegistry.List(ctx)
	testutil.FailErr(t, "list projects for source release", err)
	for i := range projects {
		sourcefeed.StopProjectWatch(ctx, projects[i].ID)
	}
	// Events delivered before the watches stopped may have scheduled work.
	drainBackground(t, srv)
	for i := range projects {
		for _, root := range projects[i].Roots {
			path := strings.TrimSpace(root.Path)
			if path == "" {
				continue
			}
			testutil.FailErr(t, "release catalog root "+path, sourcecatalog.Process().Trees.ReleaseTreeRoot(ctx, path))
		}
	}
}

func drainBackground(t *testing.T, srv *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.WaitForBackground(ctx)
	testutil.FailErr(t, "drain server background work", ctx.Err())
}

// testUserNotices loads the bundled user-notice catalog.
func testUserNotices(t *testing.T) *usernotice.Catalog {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	cfg, err := usernotice.LoadNoticeDir(filepath.Join(filepath.Dir(file), "..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	if err != nil {
		t.Fatalf("load user notices: %v", err)
	}
	return usernotice.NewCatalog(cfg)
}

func startTestHTTPServer(t *testing.T, srv *Server) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpServer := &http.Server{Handler: srv}
	go httpServer.Serve(listener)
	t.Cleanup(func() {
		_ = httpServer.Close()
		http.DefaultClient.CloseIdleConnections()
	})
	return fmt.Sprintf("http://%s", listener.Addr().String())
}

func createProjectForTest(t *testing.T, srv *Server, dir string) wire.Project {
	t.Helper()
	var resp wire.Project
	decodeCreateProjectForTest(t, srv, dir, &resp)
	return resp
}

func decodeCreateProjectForTest(t *testing.T, srv *Server, dir string, out *wire.Project) {
	t.Helper()
	body := fmt.Sprintf(`{"roots":[{"path":%q}]}`, dir)
	req := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("decode create project response: %v", err)
	}
}

func createSessionAtPathOnServer(t *testing.T, srv *Server, dir string, posture wire.SessionPosture) wire.Session {
	t.Helper()
	p := createProjectForTest(t, srv, dir)
	body := fmt.Sprintf(`{"project_id":%q,"posture":%q}`, p.ID, posture)
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body=%s", w.Code, w.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	drainBackground(t, srv)
	sess.Status = wire.SessionStatusIdle
	return sess
}

// waitSessionPrepared polls a newly created session until preparation ends;
// creation answers 202 while the workspace is still being prepared.

func promptJSON(text string) string {
	return fmt.Sprintf(`{"operation_id":%q,"text":%q}`, uuid.NewString(), text)
}

// waitForSessionIdle waits until no admitted prompt is pending and no turn runs;
// prompt_pending covers the gap between admission and the turn going busy.

func testMockConfig(t *testing.T) *llm.MockConfig {
	t.Helper()
	cfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}

func assertErrorResponse(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, wantStatus, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if string(resp.Code) != wantCode {
		t.Fatalf("code = %q, want %q; error=%q", resp.Code, wantCode, resp.Message)
	}
	if strings.TrimSpace(resp.Message) == "" {
		t.Fatal("expected non-empty error message")
	}
}

// withTrustSurfaces adds a trust-surface store when the settings lack one.

// withProjectMCP serves reg with its project-layer gate open.
