package api

import (
	"bufio"
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
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
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

func authedHTTPPost(url, contentType, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	WithTestAuth(req)
	return http.DefaultClient.Do(req)
}

func authedHTTPGet(url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	WithTestAuth(req)
	return http.DefaultClient.Do(req)
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
	mgr := session.NewManager(store, mock, reg, settings.DefaultSessionLimits())
	mgr.SetDataDir(t.TempDir())
	mgr.SetToolInvoker(testtool.RegistryInvoker{Registry: reg})
	// Stub bindings do not expose coordinator tools.
	wireTestBindingRegistry(t)
	return newServerForTest(t, Dependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}, opts...)
}

// Ambient attach requires sessions and workflows in the same database.
func newTestServerWithWorkflows(t *testing.T, opts ...testDeps) *Server {
	t.Helper()
	return newTestServerWithWorkflowRegistry(t, tools.NewStubRegistry(), opts...)
}

func newTestServerWithWorkflowRegistry(t *testing.T, reg tools.ToolRegistry, opts ...testDeps) *Server {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	sessions := store.NewSQL(sqlDB)
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewManager(sessions, mock, reg, settings.DefaultSessionLimits())
	mgr.SetDataDir(t.TempDir())
	mgr.SetToolInvoker(testtool.RegistryInvoker{Registry: reg})
	wireTestBindingRegistry(t)

	registry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs", err)
	runs := workflow.NewSQLStore(sqlDB)
	workflows := workflow.NewManager(runs, sessions, registry, nil)
	workflows.Resolver = workflow.ManifestResolver{}
	mgr.SetWorkflowSessionView(workflows)
	return newServerForTest(t, Dependencies{
		Store: sessions, PersonActions: personactions.New(sqlDB), Projects: project.NewSQLRegistry(sqlDB), Sessions: mgr,
		Workflows: workflows, WorkflowRuns: runs,
	}, opts...)
}

// newServerForTest builds a server with the bundled user notices after opts
// adjust deps, and stops its background work when the test ends.
func newServerForTest(t *testing.T, deps Dependencies, opts ...testDeps) *Server {
	t.Helper()
	deps.UserNotices = testUserNotices(t)
	for _, opt := range opts {
		opt(&deps)
	}
	srv := NewServer(requiredTestDeps(t, deps), nil, TestAPIToken)
	stopBackgroundOnCleanup(t, srv)
	return srv
}

// withSQLProjects keeps projects in the database the filled source ledger
// writes to, so the files a read tracks can reference their project.
func withSQLProjects(t *testing.T) testDeps {
	t.Helper()
	database := testdbfixture.Open(t, "projects.db")
	return func(d *Dependencies) {
		d.Database = database
		d.Projects = project.NewSQLRegistry(database)
	}
}

// requiredTestDeps supplies every dependency the route families require that
// deps leaves unset.
func requiredTestDeps(t *testing.T, deps Dependencies) Dependencies {
	t.Helper()
	fill := apitestdeps.Deps{
		ApprovalGate:      deps.ApprovalGate,
		ApprovalDecisions: deps.Authority.ApprovalDecisions,
		Database:          deps.Database, Store: deps.Store, Projects: deps.Projects, Sessions: deps.Sessions,
		Settings: deps.Settings, Invocations: deps.Invocations, MutationGate: deps.MutationGate,
		ManagedSecrets: deps.ManagedSecrets, SecretIgnores: deps.SecretIgnores, SourceLedger: deps.SourceLedger,
		SourceMutations: deps.SourceMutations, FileOperations: deps.FileOperations,
		EditorDocuments: deps.EditorDocuments, FileBriefings: deps.FileBriefings, Workflows: deps.Workflows,
		WorkflowRuns: deps.WorkflowRuns, WorkflowComposer: deps.WorkflowComposer, WorkflowPersister: deps.WorkflowPersister,
		Blueprints: deps.Blueprints, ScanCoordinator: deps.ScanCoordinator, ScanCadence: deps.ScanCadence,
		PublishDetections: deps.PublishDetections, DataDir: deps.DataDir, ModuleRoot: deps.ModuleRoot,
		MCP: deps.MCP, ExtensionViews: deps.ExtensionViews,
		ContributionReceipts: deps.Contributions.Receipts, ContributionAuthority: deps.Contributions.Authority,
		LLM: deps.LLM, CostTracker: deps.CostTracker, Events: deps.Events, EventPublisher: deps.EventPublisher,
		HostIdentity: deps.HostIdentity, Checkpoints: deps.Checkpoints, ProgressStore: deps.ProgressStore,
		VisualStore: deps.VisualStore, HistoryStorage: deps.HistoryStorage, HostResources: deps.HostResources,
		HostPower: deps.HostPower, Pricing: deps.Pricing, AgentPresence: deps.AgentPresence, Workers: deps.Workers,
		WorkerCancel: deps.WorkerCancel, Delegations: deps.Delegations, Board: deps.Board,
		HarnessWorkers: deps.HarnessWorkers, WebResearch: deps.WebResearch, WebDiscoverer: deps.WebDiscoverer,
	}
	apitestdeps.Fill(t, &fill)
	deps.ApprovalGate = fill.ApprovalGate
	deps.Authority.ApprovalDecisions = fill.ApprovalDecisions
	deps.Database, deps.Store, deps.Projects, deps.Sessions = fill.Database, fill.Store, fill.Projects, fill.Sessions
	deps.Settings, deps.Invocations, deps.MutationGate = fill.Settings, fill.Invocations, fill.MutationGate
	deps.ManagedSecrets, deps.SecretIgnores, deps.SourceLedger = fill.ManagedSecrets, fill.SecretIgnores, fill.SourceLedger
	deps.SourceMutations, deps.FileOperations = fill.SourceMutations, fill.FileOperations
	deps.EditorDocuments, deps.FileBriefings, deps.Workflows = fill.EditorDocuments, fill.FileBriefings, fill.Workflows
	deps.WorkflowRuns, deps.WorkflowComposer, deps.WorkflowPersister = fill.WorkflowRuns, fill.WorkflowComposer, fill.WorkflowPersister
	deps.Blueprints, deps.ScanCoordinator, deps.ScanCadence = fill.Blueprints, fill.ScanCoordinator, fill.ScanCadence
	deps.PublishDetections, deps.DataDir, deps.ModuleRoot = fill.PublishDetections, fill.DataDir, fill.ModuleRoot
	deps.MCP, deps.ExtensionViews = fill.MCP, fill.ExtensionViews
	deps.Contributions.Receipts, deps.Contributions.Authority = fill.ContributionReceipts, fill.ContributionAuthority
	deps.LLM, deps.CostTracker, deps.Events, deps.EventPublisher = fill.LLM, fill.CostTracker, fill.Events, fill.EventPublisher
	deps.HostIdentity, deps.Checkpoints, deps.ProgressStore = fill.HostIdentity, fill.Checkpoints, fill.ProgressStore
	deps.VisualStore, deps.HistoryStorage, deps.HostResources = fill.VisualStore, fill.HistoryStorage, fill.HostResources
	deps.HostPower, deps.Pricing, deps.AgentPresence = fill.HostPower, fill.Pricing, fill.AgentPresence
	deps.Workers, deps.WorkerCancel, deps.Delegations, deps.Board = fill.Workers, fill.WorkerCancel, fill.Delegations, fill.Board
	deps.HarnessWorkers = fill.HarnessWorkers
	deps.WebResearch, deps.WebDiscoverer = fill.WebResearch, fill.WebDiscoverer
	return deps
}

type hiddenToolRegistry struct {
	tools.ToolRegistry
	hidden map[string]struct{}
}

func withoutTools(reg tools.ToolRegistry, names ...string) tools.ToolRegistry {
	hidden := make(map[string]struct{}, len(names))
	for _, name := range names {
		hidden[name] = struct{}{}
	}
	return hiddenToolRegistry{ToolRegistry: reg, hidden: hidden}
}

func (r hiddenToolRegistry) Definition(name string) (tools.Definition, bool) {
	if _, hidden := r.hidden[name]; hidden {
		return tools.Definition{}, false
	}
	return r.ToolRegistry.Definition(name)
}

func (r hiddenToolRegistry) Run(ctx context.Context, name string, args map[string]any, tctx tools.ToolContext) (string, error) {
	if _, hidden := r.hidden[name]; hidden {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return r.ToolRegistry.Run(ctx, name, args, tctx)
}

func (r hiddenToolRegistry) List() []tools.ToolMeta {
	listed := r.ToolRegistry.List()
	visible := listed[:0]
	for _, meta := range listed {
		if _, hidden := r.hidden[meta.Name]; !hidden {
			visible = append(visible, meta)
		}
	}
	return visible
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
	if srv.projectRegistry == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	projects, err := srv.projectRegistry.List(ctx)
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
			testutil.FailErr(t, "release catalog root "+path, sourcecatalog.Process().ReleaseTreeRoot(ctx, path))
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
	t.Cleanup(func() { _ = httpServer.Close() })
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

func createTestSession(t *testing.T, baseURL, projectDir string) wire.Session {
	t.Helper()
	projBody := fmt.Sprintf(`{"roots":[{"path":%q}]}`, projectDir)
	projResp, err := authedHTTPPost(baseURL+"/v1/projects", "application/json", projBody)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	defer projResp.Body.Close()
	if projResp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", projResp.StatusCode, string(readBody(t, projResp)))
	}
	var proj wire.Project
	if err := json.NewDecoder(projResp.Body).Decode(&proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/sessions", strings.NewReader(
		fmt.Sprintf(`{"project_id":%q,"posture":"build"}`, proj.ID)))
	if err != nil {
		t.Fatalf("create session request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", resp.StatusCode, string(readBody(t, resp)))
	}
	var sess wire.Session
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for sess.Status == wire.SessionStatusPreparing && time.Now().Before(deadline) {
		readyResp, getErr := authedHTTPGet(baseURL + "/v1/sessions/" + sess.ID)
		if getErr != nil {
			t.Fatalf("wait for session preparation: %v", getErr)
		}
		if readyResp.StatusCode != http.StatusOK {
			readyResp.Body.Close()
			t.Fatalf("wait for session preparation status = %d", readyResp.StatusCode)
		}
		if decodeErr := json.NewDecoder(readyResp.Body).Decode(&sess); decodeErr != nil {
			readyResp.Body.Close()
			t.Fatalf("decode prepared session: %v", decodeErr)
		}
		readyResp.Body.Close()
		if sess.Status == wire.SessionStatusPreparing {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if sess.Status == wire.SessionStatusPreparing {
		t.Fatal("session preparation did not complete")
	}
	return sess
}

func acceptPrompt(t *testing.T, baseURL, sessionID, text string) {
	t.Helper()
	baseline := len(listMessagesAtURL(t, baseURL, sessionID))
	acceptedAt := time.Now()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/sessions/"+sessionID+"/prompts",
		strings.NewReader(promptJSON(text)))
	if err != nil {
		t.Fatalf("prompt request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt status = %d body = %s", resp.StatusCode, string(readBody(t, resp)))
	}
	var accepted wire.PromptAcceptedResponse
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode prompt accept: %v", err)
	}
	if accepted.Status != "queued" {
		t.Fatalf("prompt accept status = %q", accepted.Status)
	}
	waitPromptTurnAtURL(t, baseURL, sessionID, baseline, acceptedAt, 10*time.Second)
}

func promptJSON(text string) string {
	return fmt.Sprintf(`{"operation_id":%q,"text":%q}`, uuid.NewString(), text)
}

func waitForSessionIdle(t *testing.T, baseURL, sessionID string, timeout time.Duration) wire.Session {
	t.Helper()
	return waitPromptTurnAtURL(t, baseURL, sessionID, len(listMessagesAtURL(t, baseURL, sessionID)), time.Now(), timeout)
}

func waitPromptTurnAtURL(t *testing.T, baseURL, sessionID string, baseline int, acceptedAt time.Time, timeout time.Duration) wire.Session {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	sawBusy := false
	for time.Now().Before(deadline) {
		sess := getSessionAtURL(t, baseURL, sessionID)
		if sess.Status == wire.SessionStatusBusy {
			sawBusy = true
		}
		msgs := listMessagesAtURL(t, baseURL, sessionID)
		if promptTurnSettled(sess, msgs, baseline, sawBusy, acceptedAt) {
			return sess
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session %s prompt turn did not complete within %s", sessionID, timeout)
	return wire.Session{}
}

func promptTurnSettled(sess wire.Session, msgs []wire.Message, baseline int, sawBusy bool, acceptedAt time.Time) bool {
	if sess.Status != wire.SessionStatusIdle {
		return false
	}
	if sawBusy {
		return true
	}
	for i := baseline; i < len(msgs); i++ {
		if msgs[i].Role != wire.MessageRoleUser {
			return true
		}
	}
	return time.Since(acceptedAt) >= 50*time.Millisecond
}

func getSessionAtURL(t *testing.T, baseURL, sessionID string) wire.Session {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID, nil)
	if err != nil {
		t.Fatalf("get session request: %v", err)
	}
	WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d body = %s", resp.StatusCode, body)
	}
	var sess wire.Session
	if err := json.Unmarshal(body, &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess
}

func listMessagesAtURL(t *testing.T, baseURL, sessionID string) []wire.Message {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/messages", nil)
	if err != nil {
		t.Fatalf("list messages request: %v", err)
	}
	WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list messages status = %d body = %s", resp.StatusCode, string(readBody(t, resp)))
	}
	var page wire.SessionTranscriptPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	return page.Messages
}

func waitForAssistantStream(t *testing.T, baseURL, sessionID string, timeout time.Duration) (messageID, streamURL string) {
	t.Helper()
	waitForSessionIdle(t, baseURL, sessionID, timeout)
	msgs := listMessagesAtURL(t, baseURL, sessionID)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == wire.MessageRoleAssistant && strings.TrimSpace(msgs[i].ID) != "" {
			messageID = msgs[i].ID
			streamURL = fmt.Sprintf("/v1/sessions/%s/stream?message=%s", sessionID, messageID)
			return messageID, streamURL
		}
	}
	t.Fatalf("no assistant message for session %s", sessionID)
	return "", ""
}

func readSSEStream(t *testing.T, streamURL string) (content string, sawDone bool) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	var tokens []string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk wire.PromptStreamChunk
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("decode chunk: %v", err)
		}
		if chunk.Token != "" {
			tokens = append(tokens, chunk.Token)
		}
		if chunk.Done {
			sawDone = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	return strings.Join(tokens, ""), sawDone
}

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
func withTrustSurfaces(t *testing.T) testDeps {
	t.Helper()
	return func(d *Dependencies) {
		if d.Settings == nil {
			d.Settings = &settings.Service{}
		}
		if d.Settings.TrustSurfaces != nil {
			return
		}
		surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
		testutil.FailErr(t, "trust surfaces store", err)
		d.Settings.TrustSurfaces = surfaces
	}
}

// withProjectMCP serves reg with its project-layer gate open.
func withProjectMCP(t *testing.T, reg *mcp.RegistryImpl) testDeps {
	t.Helper()
	trust := withTrustSurfaces(t)
	return func(d *Dependencies) {
		trust(d)
		reg.SetProjectOverlayGate(func(context.Context, string) bool { return true })
		d.MCP = reg
	}
}
