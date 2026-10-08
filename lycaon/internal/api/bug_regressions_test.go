package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestStartWorkflowUnknownIDReturns404NotInternalError(t *testing.T) {
	srv := newTestServerWithWorkflows(t)
	sess := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureSpec)

	body := `{"operation_id":"` + uuid.NewString() + `","workflow_id":"nope-xyzzy","workflow_version":"1.0.0"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "workflow_not_found" {
		t.Fatalf("code = %q, want workflow_not_found", resp.Code)
	}
}

func TestCreateBlueprintInvalidPathReturns400NotInternalError(t *testing.T) {
	srv := newTestServer(t, func(d *Dependencies) { d.Blueprints = &blueprint.Manager{} })
	opened := createProjectForTest(t, srv, t.TempDir())

	body := `{"title":"Plan","path":"outside/plan.md"}`
	req := newAuthedRequest(http.MethodPost, "/v1/projects/"+opened.ID+"/blueprints", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "invalid_request" {
		t.Fatalf("code = %q, want invalid_request", resp.Code)
	}
}

// blueprintCall serves one authed blueprint request and checks its status.
func blueprintCall(t *testing.T, srv *Server, method, target, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := newAuthedRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: status = %d, want %d; body=%s", method, target, w.Code, want, w.Body.String())
	}
	return w
}

func TestBlueprintRoutesEditAndRemoveABlueprint(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithWorkflows(t, func(d *Dependencies) {
		d.Blueprints = blueprint.NewManager(blueprint.NewFileStoreForTest(dir))
	})
	opened := createProjectForTest(t, srv, dir)
	base := "/v1/projects/" + opened.ID + "/blueprints"

	var created wire.Blueprint
	w := blueprintCall(t, srv, http.MethodPost, base, `{"title":"Plan"}`, http.StatusCreated)
	testutil.FailErr(t, "decode created", json.Unmarshal(w.Body.Bytes(), &created))

	var listed wire.BlueprintListResponse
	w = blueprintCall(t, srv, http.MethodGet, base, "", http.StatusOK)
	testutil.FailErr(t, "decode list", json.Unmarshal(w.Body.Bytes(), &listed))
	if len(listed.Blueprints) != 1 || listed.Blueprints[0].ID != created.ID {
		t.Fatalf("list = %+v, want the created blueprint", listed.Blueprints)
	}

	w = blueprintCall(t, srv, http.MethodGet, base+"?path="+created.Path, "", http.StatusOK)
	testutil.FailErr(t, "decode path filter", json.Unmarshal(w.Body.Bytes(), &listed))
	if len(listed.Blueprints) != 1 || listed.Blueprints[0].Path != created.Path {
		t.Fatalf("path filter = %+v, want %q", listed.Blueprints, created.Path)
	}
	blueprintCall(t, srv, http.MethodGet, base+"?path=", "", http.StatusBadRequest)

	item := base + "/" + created.ID
	var got wire.Blueprint
	w = blueprintCall(t, srv, http.MethodGet, item, "", http.StatusOK)
	testutil.FailErr(t, "decode get", json.Unmarshal(w.Body.Bytes(), &got))
	if got.ID != created.ID {
		t.Fatalf("get id = %q, want %q", got.ID, created.ID)
	}

	blueprintCall(t, srv, http.MethodPatch, item, `{}`, http.StatusBadRequest)
	blueprintCall(t, srv, http.MethodPatch, item, `{"title":"   "}`, http.StatusBadRequest)
	var updated wire.Blueprint
	w = blueprintCall(t, srv, http.MethodPatch, item, `{"title":"Revised plan","content":"# Revised plan\n"}`, http.StatusOK)
	testutil.FailErr(t, "decode update", json.Unmarshal(w.Body.Bytes(), &updated))
	if updated.Title != "Revised plan" || !strings.Contains(updated.Content, "# Revised plan") {
		t.Fatalf("updated = %q / %q, want the new title and content", updated.Title, updated.Content)
	}

	blueprintCall(t, srv, http.MethodDelete, item, "", http.StatusNoContent)
	blueprintCall(t, srv, http.MethodGet, item, "", http.StatusNotFound)
	blueprintCall(t, srv, http.MethodDelete, item, "", http.StatusNotFound)
}

// errScanCoordinator fails every read with getErr; summary, when set, is the
// run's identity that project-scoped routes check before reading.
type errScanCoordinator struct {
	getErr  error
	summary *wire.CodeScan
}

// withErrScanCoordinator serves scans from a coordinator that knows no rows.
func withErrScanCoordinator(d *Dependencies) {
	d.ScanCoordinator = &errScanCoordinator{getErr: sql.ErrNoRows}
}

func (e *errScanCoordinator) Summary(ctx context.Context, id string) (*wire.CodeScan, error) {
	if e.summary != nil {
		return e.summary, nil
	}
	return nil, e.getErr
}

func (e *errScanCoordinator) PublishSourceGeneration(context.Context, string) (sourcesnapshot.Snapshot, string, error) {
	return sourcesnapshot.Snapshot{}, "", e.getErr
}
func (e *errScanCoordinator) Enqueue(ctx context.Context, req scan.EnqueueRequest) (*wire.CodeScan, error) {
	return &wire.CodeScan{ID: "x"}, nil
}
func (e *errScanCoordinator) Get(ctx context.Context, id string) (*wire.CodeScan, error) {
	return nil, e.getErr
}
func (e *errScanCoordinator) List(ctx context.Context, canonicalPaths []string, limit int) ([]wire.CodeScan, error) {
	return nil, nil
}
func (e *errScanCoordinator) ListPage(ctx context.Context, canonicalPaths []string, query scan.PageQuery) (wire.CodeScanPage, error) {
	return wire.CodeScanPage{}, e.getErr
}
func (e *errScanCoordinator) ListBySessionID(ctx context.Context, sessionID string) ([]wire.CodeScan, error) {
	return nil, nil
}
func (e *errScanCoordinator) ListByWorkflowRunID(ctx context.Context, workflowRunID string) ([]wire.CodeScan, error) {
	return nil, nil
}
func (e *errScanCoordinator) LatestForDelegation(ctx context.Context, delegationID string, categories []wire.ScanCategory) (*wire.CodeScan, error) {
	return nil, nil
}
func (e *errScanCoordinator) Query(ctx context.Context, req scan.QueryRequest) (*wire.ScanQueryResponse, error) {
	return nil, e.getErr
}
func (e *errScanCoordinator) Compare(ctx context.Context, _, _ string) (*scan.Comparison, error) {
	return nil, e.getErr
}
func (e *errScanCoordinator) PreviousComplete(ctx context.Context, _ wire.CodeScan) (*wire.CodeScan, error) {
	return nil, e.getErr
}

func TestScanGetUnknownIDReturns404NotInternalError(t *testing.T) {
	srv := newTestServer(t, withErrScanCoordinator)
	p := createProjectForTest(t, srv, t.TempDir())

	req := newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/scans/does-not-exist", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "scan_not_found" {
		t.Fatalf("code = %q, want scan_not_found", resp.Code)
	}
}

// Empty URL path segments stop before handler dispatch.
func TestEmptyPathSegmentReturns400(t *testing.T) {
	srv := newTestServer(t, withErrScanCoordinator)
	p := createProjectForTest(t, srv, t.TempDir())

	req := newAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/scans//query", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "invalid_request" {
		t.Fatalf("code = %q, want invalid_request", resp.Code)
	}
	if resp.Title == "" || resp.Message == "" {
		t.Fatalf("expected rendered user notice on wire, got %+v", resp)
	}
	if strings.Contains(resp.Message, "empty segment") {
		t.Fatalf("debug message leaked to wire: %s", resp.Message)
	}
}

// Scan query errors omit storage details.
func TestScanQueryUnknownIDReturnsSafe404Message(t *testing.T) {
	srv := newTestServer(t, withErrScanCoordinator)
	p := createProjectForTest(t, srv, t.TempDir())

	req := newAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/scans/00000000-0000-0000-0000-000000000000/query", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "sql:") || strings.Contains(strings.ToLower(body), "no rows") {
		t.Fatalf("response leaked driver error: %s", body)
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "scan_not_found" {
		t.Fatalf("code = %q, want scan_not_found", resp.Code)
	}
}

// Invalid delegation strategies map to 400.

func newInMemoryDelegationServer(t *testing.T) *Server {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewManager(store, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	delegationStore := delegation.NewMemoryStore()
	workersCfg := worker.DefaultWorkersConfig()
	queue := worker.NewInMemoryQueue(workersCfg.Poller.MaxConcurrency)
	queue.SetWorkersConfig(workersCfg)
	delegationMgr := delegation.NewManager(delegationStore, queue, mgr, nil)
	reg := project.NewMemoryRegistry()
	delegationMgr.Projects = reg
	snap := &board.SnapshotBuilder{Delegations: delegationStore, Workers: queue, Repo: repotest.NewProvider(t)}
	return NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: reg, Sessions: mgr,
		Delegations: delegationMgr, Workers: queue, Board: snap,
	}), nil, TestAPIToken)
}

func TestCreateDelegationInvalidStrategyReturns400(t *testing.T) {
	srv := newInMemoryDelegationServer(t)
	dir := t.TempDir()
	opened := createProjectForTest(t, srv, dir)

	body := fmt.Sprintf(`{"operation_id":%q,"project_id":%q,"task":"do it","strategy":"single"}`, uuid.NewString(), opened.ID)
	req := newAuthedRequest(http.MethodPost, "/v1/delegations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.Message, "strategy") {
		t.Fatalf("error = %q, expected to mention strategy", resp.Message)
	}
}

func TestCreateDelegationInvalidInspectModeReturns400(t *testing.T) {
	srv := newInMemoryDelegationServer(t)
	dir := t.TempDir()
	opened := createProjectForTest(t, srv, dir)

	body := fmt.Sprintf(`{"project_id":%q,"task":"do it","strategy":"file-based","inspect_mode":"fast"}`, opened.ID)
	req := newAuthedRequest(http.MethodPost, "/v1/delegations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// Provider base URLs require HTTP or HTTPS.

func TestCreateProviderRejectsInvalidBaseURL(t *testing.T) {
	_, base := newProviderTestServer(t)
	cases := []struct {
		name string
		body string
	}{
		{"junk url", `{"id":"test-prov","base_url":"::not_a_url::","models":[{"id":"m"}]}`},
		{"no scheme", `{"id":"test-prov","base_url":"api.example.com","models":[{"id":"m"}]}`},
		{"ftp scheme", `{"id":"test-prov","base_url":"ftp://api.example.com","models":[{"id":"m"}]}`},
		{"empty model id", `{"id":"test-prov","base_url":"https://api.example.com","models":[{"id":""}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/v1/providers", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			WithTestAuth(req)
			resp, err := http.DefaultClient.Do(req)
			testutil.FailErr(t, "http.DefaultClient.Do failed", err)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d for body %q", resp.StatusCode, tc.body)
			}
		})
	}
}

// List endpoints encode empty collections as arrays.

func TestWorkersEmptyListReturnsArrayNotNull(t *testing.T) {
	srv := newInMemoryDelegationServer(t)
	projectDir := t.TempDir()
	opened := createProjectForTest(t, srv, projectDir)

	req := newAuthedRequest(http.MethodGet, "/v1/workers?project_id="+opened.ID+"&session_id=sess-empty", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var res wire.WorkerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Workers == nil {
		t.Fatalf("/v1/workers returned nil workers slice")
	}
}

func TestDelegationLegsEmptyListReturnsArrayNotNull(t *testing.T) {
	srv := newInMemoryDelegationServer(t)
	dir := t.TempDir()
	opened := createProjectForTest(t, srv, dir)
	body := fmt.Sprintf(`{"operation_id":%q,"project_id":%q,"task":"x","strategy":"file-based"}`, uuid.NewString(), opened.ID)
	req := newAuthedRequest(http.MethodPost, "/v1/delegations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var dep wire.Delegation
	if err := json.Unmarshal(w.Body.Bytes(), &dep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	w = httptest.NewRecorder()
	req = newAuthedRequest(http.MethodGet, "/v1/delegations/"+dep.ID+"/legs", nil)
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("legs status = %d body=%s", w.Code, w.Body.String())
	}
	var legRes wire.DelegationLegListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &legRes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if legRes.Legs == nil {
		t.Fatalf("/v1/delegations/{id}/legs returned nil legs slice")
	}
}

func TestListWorkersFiltersBySessionID(t *testing.T) {
	srv := newInMemoryDelegationServer(t)
	projectDir := t.TempDir()
	opened := createProjectForTest(t, srv, projectDir)

	ctx := context.Background()
	now := time.Now().UTC()
	queue := srv.workers.(*worker.InMemoryQueue)
	if _, err := queue.Enqueue(ctx, wire.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ID:              "worker-a",
		ParentSessionID: "sess-a",
		ProjectID:       opened.ID,
		WorkspacePath:   primaryRootPath(opened),
		AgentType:       "implementer",
		Status:          wire.WorkerStatusRunning,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if _, err := queue.Enqueue(ctx, wire.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ID:              "worker-b",
		ParentSessionID: "sess-b",
		ProjectID:       opened.ID,
		WorkspacePath:   primaryRootPath(opened),
		AgentType:       "implementer",
		Status:          wire.WorkerStatusRunning,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	req := newAuthedRequest(
		http.MethodGet,
		"/v1/workers?project_id="+opened.ID+"&session_id=sess-a",
		nil,
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var filterRes wire.WorkerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &filterRes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	filtered := filterRes.Workers
	if len(filtered) != 1 || filtered[0].ID != "worker-a" {
		t.Fatalf("filtered = %+v, want [worker-a]", filtered)
	}

	req = newAuthedRequest(http.MethodGet, "/v1/workers?project_id="+opened.ID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400 without session_id", w.Code, w.Body.String())
	}
}

func (e *errScanCoordinator) ResolveID(ctx context.Context, path, prefix string) (string, error) {
	return "", nil
}

func (c *errScanCoordinator) SnapshotStore() *sourcesnapshot.Store { return nil }
