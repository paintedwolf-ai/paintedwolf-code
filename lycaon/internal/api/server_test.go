package api

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWriteJSONPreflightsEncodingBeforeStatus(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	httpio.WriteJSON(rec, http.StatusOK, map[string]float64{"value": math.NaN()})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var response wire.ErrorResponse
	testutil.FailErr(t, "decode response", json.Unmarshal(rec.Body.Bytes(), &response))
	if response.Code != wire.ApiErrorCodeInternalError {
		t.Errorf("code = %q, want %q", response.Code, wire.ApiErrorCodeInternalError)
	}
	if response.Message == "" {
		t.Error("error message is empty")
	}
}

func TestWriteJSONDeclaresExactResponseLength(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	httpio.WriteJSON(rec, http.StatusOK, map[string]string{"status": "ready"})

	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
}

// resolveTestPath matches the canonical path stored by the registry.
func resolveTestPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", p, err)
	}
	return r
}

func TestCreateSessionByProjectIDNotFound(t *testing.T) {
	srv := newTestServer(t)
	body := `{"project_id":"00000000-0000-4000-8000-000000000001","posture":"spec"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assertErrorResponse(t, w, http.StatusNotFound, "project_not_found")
}

func TestCreateSessionInvalidJSON(t *testing.T) {
	srv := newTestServer(t)
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assertErrorResponse(t, w, http.StatusBadRequest, "invalid_json")
}

func TestCreateSessionRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	p := createProjectForTest(t, srv, dir)

	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"project_id":"`+p.ID+`","posture":"spec","unexpected":true}`,
	))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusBadRequest, "invalid_json")
}

func TestCreateSessionResolvesProjectIDFromCreateProject(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)

	openReq := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(
		`{"roots":[{"path":"`+dir+`"}]}`,
	))
	openRec := httptest.NewRecorder()
	srv.ServeHTTP(openRec, openReq)

	var opened wire.Project
	if err := json.Unmarshal(openRec.Body.Bytes(), &opened); err != nil {
		t.Fatalf("decode create project: %v", err)
	}
	if openRec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", openRec.Code, openRec.Body.String())
	}

	createReq := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"project_id":"`+opened.ID+`","posture":"spec"}`,
	))
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", createRec.Code, createRec.Body.String())
	}

	var sess wire.Session
	if err := json.Unmarshal(createRec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sess.ProjectID != opened.ID {
		t.Fatalf("project_id = %q, want %q", sess.ProjectID, opened.ID)
	}
}

func TestCreateProjectInvalidJSON(t *testing.T) {
	srv := newTestServer(t)
	req := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(`{`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assertErrorResponse(t, w, http.StatusBadRequest, "invalid_json")
}

func TestPatchProjectAppliesAllPresentFields(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	// The draft workspace lives under the config dir, and the process source
	// catalog indexes it into <config>/cache; finish that before TempDir removal.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		testutil.FailErr(t, "drain source catalog", sourcecatalog.Process().Drain(ctx))
	})
	srv := newTestServer(t)
	createReq := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(
		`{"name":"Draft","roots":[],"draft":true}`,
	))
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", createRec.Code, createRec.Body.String())
	}
	var created wire.Project
	testutil.FailErr(t, "decode created project", json.Unmarshal(createRec.Body.Bytes(), &created))

	patchReq := newAuthedRequest(http.MethodPatch, "/v1/projects/"+created.ID, strings.NewReader(
		`{"name":"Release","starred":true}`,
	))
	patchRec := httptest.NewRecorder()
	srv.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch project status = %d body = %s", patchRec.Code, patchRec.Body.String())
	}
	var patched wire.Project
	testutil.FailErr(t, "decode patched project", json.Unmarshal(patchRec.Body.Bytes(), &patched))
	if patched.Name == nil || *patched.Name != "Release" || !patched.Starred || !patched.IsDraft {
		t.Fatalf("patched project = %#v", patched)
	}
}

func TestCreateProjectInvalidPath(t *testing.T) {
	srv := newTestServer(t)
	file := filepath.Join(t.TempDir(), "fixture.txt")
	testutil.FailErr(t, "write non-directory fixture", os.WriteFile(file, []byte("synthetic"), 0o600))
	for _, path := range []string{"", file} {
		t.Run(path, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"roots": []map[string]string{{"path": path}}})
			testutil.FailErr(t, "encode invalid folder request", err)
			req := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			assertErrorResponse(t, w, http.StatusBadRequest, "invalid_path")
			var response wire.ErrorResponse
			testutil.FailErr(t, "decode invalid folder response", json.Unmarshal(w.Body.Bytes(), &response))
			if !strings.Contains(response.SuggestedAction, "existing folder") || strings.Contains(response.Message, "no more detail") {
				t.Fatalf("invalid folder lost its recovery guidance: %+v", response)
			}
		})
	}
}

func TestCreateSessionProjectNotFound(t *testing.T) {
	srv := newTestServer(t)
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"project_id":"00000000-0000-4000-8000-000000009999","posture":"spec"}`,
	))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %q", resp["status"])
	}
	if resp["version"] != version.Version {
		t.Fatalf("version = %q want %q", resp["version"], version.Version)
	}
	if _, ok := resp["store_revision"]; !ok {
		t.Fatal("store_revision missing from health response")
	}
	sv, ok := resp["schema_version"].(float64)
	if !ok {
		t.Fatalf("schema_version missing or wrong type: %#v", resp["schema_version"])
	}
	if int(sv) != db.SchemaVersion {
		t.Fatalf("schema_version = %v want %d", sv, db.SchemaVersion)
	}
	if _, ok := resp["min_den_version"]; ok {
		t.Fatal("min_den_version should be omitted when unset")
	}
}

func TestHealthEndpointStoreRevision(t *testing.T) {
	srv := newTestServer(t, func(d *Dependencies) { d.StoreRevision = 42 })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp struct {
		StoreRevision uint64 `json:"store_revision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StoreRevision != 42 {
		t.Fatalf("store_revision = %d, want 42", resp.StoreRevision)
	}
}

func TestHealthEndpointMinDenVersion(t *testing.T) {
	srv := newTestServer(t, func(d *Dependencies) { d.MinDenVersion = "1.2.3" })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp struct {
		MinDenVersion string `json:"min_den_version"`
		SchemaVersion int    `json:"schema_version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.MinDenVersion != "1.2.3" {
		t.Fatalf("min_den_version = %q, want 1.2.3", resp.MinDenVersion)
	}
	if resp.SchemaVersion != db.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", resp.SchemaVersion, db.SchemaVersion)
	}
}

func TestHealthEndpointPreviousAppVersion(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var ordinary map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &ordinary); err != nil {
		t.Fatalf("decode ordinary: %v", err)
	}
	if _, ok := ordinary["previous_app_version"]; ok {
		t.Fatal("previous_app_version must be omitted on an ordinary boot")
	}

	upgraded := newTestServer(t, func(d *Dependencies) { d.PreviousAppVersion = "0.0.1" })
	w = httptest.NewRecorder()
	upgraded.ServeHTTP(w, req)
	var upgrade struct {
		PreviousAppVersion string `json:"previous_app_version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &upgrade); err != nil {
		t.Fatalf("decode upgrade: %v", err)
	}
	if upgrade.PreviousAppVersion != "0.0.1" {
		t.Fatalf("previous_app_version = %q want 0.0.1", upgrade.PreviousAppVersion)
	}
}

func TestCreateSessionByProjectIDHTTP(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)
	if sess.ID == "" {
		t.Fatal("expected session ID")
	}
	if sess.WorkspacePath != resolveTestPath(t, dir) {
		t.Fatalf("project_dir = %q", sess.WorkspacePath)
	}
	if sess.Posture != wire.SessionPostureBuild {
		t.Fatalf("mode = %q", sess.Posture)
	}
}

func TestCreateSessionByProjectID(t *testing.T) {
	dir := t.TempDir()
	reg := project.NewMemoryRegistry()
	opened, err := project.CreateWithRoot(t.Context(), reg, dir)
	if err != nil {
		t.Fatalf("open project: %v", err)
	}

	store := store.NewMemory()
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: reg,
		Sessions: session.NewManager(store, llm.NewMockProvider(testMockConfig(t)), tools.NewStubRegistry(), settings.DefaultSessionLimits()),
	}), nil, TestAPIToken)
	body := `{"project_id":"` + opened.ID + `","posture":"spec"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode: %v", err)
	}
	drainBackground(t, srv)
	if sess.ProjectID != opened.ID {
		t.Fatalf("project_id = %q", sess.ProjectID)
	}
	if sess.WorkspacePath != resolveTestPath(t, dir) {
		t.Fatalf("project_dir = %q", sess.WorkspacePath)
	}
}

func TestCreateSessionValidation(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name string
		body string
	}{
		{"project_dir only", `{"project_dir":"/tmp","posture":"spec"}`},
		{"neither id", `{"posture":"spec"}`},
		{"missing posture", `{"project_id":"00000000-0000-4000-8000-000000000001"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d", w.Code)
			}
		})
	}
}

func TestCreateSessionRejectsIncompleteModelOverride(t *testing.T) {
	srv := newTestServer(t)
	for _, body := range []string{
		`{"project_id":"00000000-0000-4000-8000-000000000001","posture":"spec","provider_id":"prov-a"}`,
		`{"project_id":"00000000-0000-4000-8000-000000000001","posture":"spec","model":"model-x"}`,
	} {
		req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		assertErrorResponse(t, w, http.StatusBadRequest, "invalid_request")
	}
}

func TestCreateSessionValidatesAndStoresModelOverride(t *testing.T) {
	srv, _ := newProviderTestServer(t)
	project := createProjectForTest(t, srv, t.TempDir())

	unknown := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"project_id":"`+project.ID+`","posture":"spec","provider_id":"prov-a","model":"missing"}`,
	))
	unknownRec := httptest.NewRecorder()
	srv.ServeHTTP(unknownRec, unknown)
	assertErrorResponse(t, unknownRec, http.StatusBadRequest, "invalid_request")

	valid := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"project_id":"`+project.ID+`","posture":"spec","provider_id":"prov-a","model":"model-x"}`,
	))
	validRec := httptest.NewRecorder()
	srv.ServeHTTP(validRec, valid)
	if validRec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", validRec.Code, validRec.Body.String())
	}
	var created wire.Session
	testutil.FailErr(t, "decode session", json.Unmarshal(validRec.Body.Bytes(), &created))
	if created.ProviderID != "prov-a" || created.Model != "model-x" {
		t.Fatalf("model override = %q/%q", created.ProviderID, created.Model)
	}
}

func TestGetSession(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	created := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	getReq := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("status = %d", getRec.Code)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	srv := newTestServer(t)

	req := newAuthedRequest(http.MethodGet, "/v1/sessions/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestGetSessionMessages(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	created := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	msgsReq := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages", nil)
	msgsRec := httptest.NewRecorder()
	srv.ServeHTTP(msgsRec, msgsReq)

	if msgsRec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", msgsRec.Code, msgsRec.Body.String())
	}
	var page wire.SessionTranscriptPage
	if err := json.Unmarshal(msgsRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if page.Messages == nil {
		t.Fatal("expected non-nil empty array")
	}
}

func TestGetSessionMessagesPagination(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	created := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	msgsReq := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?limit=5", nil)
	msgsRec := httptest.NewRecorder()
	srv.ServeHTTP(msgsRec, msgsReq)
	if msgsRec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", msgsRec.Code, msgsRec.Body.String())
	}
	var page wire.SessionTranscriptPage
	if err := json.Unmarshal(msgsRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Messages == nil {
		t.Fatal("expected non-nil messages")
	}
	if page.BeforeCursor != "" || page.AfterCursor != "" {
		t.Fatalf("empty session cursors: before=%v after=%v", page.BeforeCursor, page.AfterCursor)
	}

	both := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?before=1&after=2", nil)
	bothRec := httptest.NewRecorder()
	srv.ServeHTTP(bothRec, both)
	if bothRec.Code != http.StatusBadRequest {
		t.Fatalf("both cursors status = %d, want 400", bothRec.Code)
	}
}

// A window opens beside a message the client holds, or at either end.
func TestGetSessionMessagesPositionsWindows(t *testing.T) {
	srv := newTestServer(t)
	created := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
	for _, content := range []string{"one", "two", "three", "four", "five"} {
		testutil.FailErr(t, "append message", srv.sessions.AppendAndPublishMessages(
			t.Context(), created.ID, wire.Message{Role: wire.MessageRoleAssistant, Content: content}))
	}
	read := func(query string, wantStatus int) wire.SessionTranscriptPage {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?"+query, nil))
		if rec.Code != wantStatus {
			t.Fatalf("%s: status = %d body=%s", query, rec.Code, rec.Body.String())
		}
		var page wire.SessionTranscriptPage
		if wantStatus == http.StatusOK {
			testutil.FailErr(t, "decode "+query, json.Unmarshal(rec.Body.Bytes(), &page))
		}
		return page
	}
	contents := func(page wire.SessionTranscriptPage) string {
		out := make([]string, 0, len(page.Messages))
		for _, msg := range page.Messages {
			out = append(out, msg.Content)
		}
		return strings.Join(out, ",")
	}
	all := read("limit=10", http.StatusOK)
	ids := map[string]string{}
	for _, msg := range all.Messages {
		ids[msg.Content] = msg.ID
	}
	cases := []struct{ query, want string }{
		{"limit=2", "four,five"},
		{"limit=2&before_message_id=" + ids["four"], "two,three"},
		{"limit=2&after_message_id=" + ids["two"], "three,four"},
		{"limit=2&from=oldest", "one,two"},
	}
	for _, tc := range cases {
		if got := contents(read(tc.query, http.StatusOK)); got != tc.want {
			t.Fatalf("%s: messages = %s, want %s", tc.query, got, tc.want)
		}
	}
	if oldest := read("limit=2&from=oldest", http.StatusOK); oldest.BeforeCursor != "" || oldest.AfterCursor == "" {
		t.Fatalf("oldest window cursors: before=%q after=%q", oldest.BeforeCursor, oldest.AfterCursor)
	}
	read("before_message_id=2c0f6f8e-5d4b-4c55-9d7e-8f1b2a3c4d5e", http.StatusNotFound)
	read("from=oldest&after_message_id="+ids["two"], http.StatusBadRequest)
	read("from=middle", http.StatusBadRequest)
}

func TestGetSessionMessagesFiltersReusedChildByWorkerJob(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	created := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)
	firstJobID := "11111111-1111-4111-8111-111111111111"
	secondJobID := "22222222-2222-4222-8222-222222222222"
	testutil.FailErr(t, "append worker messages", srv.sessions.AppendAndPublishMessages(
		t.Context(), created.ID,
		wire.Message{Role: wire.MessageRoleAssistant, Content: "first", WorkerID: firstJobID},
		wire.Message{Role: wire.MessageRoleAssistant, Content: "second", WorkerID: secondJobID},
	))

	req := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?worker_id="+secondJobID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var page wire.SessionTranscriptPage
	testutil.FailErr(t, "decode worker page", json.Unmarshal(rec.Body.Bytes(), &page))
	if len(page.Messages) != 1 || page.Messages[0].Content != "second" || page.Messages[0].WorkerID != secondJobID {
		t.Fatalf("messages = %+v", page.Messages)
	}

	bad := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?worker_id=not-a-uuid", nil)
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid worker_id status = %d", badRec.Code)
	}
}

func TestGetSessionMessagesNotFound(t *testing.T) {
	srv := newTestServer(t)

	req := newAuthedRequest(http.MethodGet, "/v1/sessions/nonexistent/messages", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestCreateAndListProjects(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)

	openReq := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(
		`{"roots":[{"path":"`+dir+`"}]}`,
	))
	openRec := httptest.NewRecorder()
	srv.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", openRec.Code, openRec.Body.String())
	}

	var opened wire.Project
	if err := json.Unmarshal(openRec.Body.Bytes(), &opened); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if primaryRootPath(opened) != resolveTestPath(t, dir) {
		t.Fatalf("path = %q", primaryRootPath(opened))
	}

	listReq := newAuthedRequest(http.MethodGet, "/v1/projects", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRec.Code)
	}

	var res wire.ProjectListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	projects := res.Projects
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
}

func TestCreateProjectMissingPathNotFound(t *testing.T) {
	srv := newTestServer(t)
	missing := filepathJoin(t.TempDir(), "missing")

	openReq := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(
		`{"roots":[{"path":"`+missing+`"}]}`,
	))
	openRec := httptest.NewRecorder()
	srv.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", openRec.Code)
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	srv := newTestServer(t)
	srv.router.Get("/panic", func(w http.ResponseWriter, r *http.Request) {
		panic("intentional")
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
}

func filepathJoin(base, elem string) string {
	return strings.TrimRight(base, string(os.PathSeparator)) + string(os.PathSeparator) + elem
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// primaryRootPath mirrors the primary-or-first root selection Den applies to wire projects.
func primaryRootPath(p wire.Project) string {
	for _, r := range p.Roots {
		if r.IsPrimary {
			return r.Path
		}
	}
	if len(p.Roots) > 0 {
		return p.Roots[0].Path
	}
	return ""
}
