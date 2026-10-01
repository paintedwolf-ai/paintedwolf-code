package security

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func authedRequest(t *testing.T, method, path string, body io.Reader) *http.Request {
	t.Helper()
	if body != nil {
		raw, err := io.ReadAll(body)
		testutil.FailErr(t, "read request body", err)
		body = bytes.NewReader(mutationRequestBody(t, method, path, string(raw)))
	}
	var r *http.Request
	if body != nil {
		r = httptest.NewRequestWithContext(t.Context(), method, path, body)
		// Endpoints validate the representation of requests carrying bytes.
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequestWithContext(t.Context(), method, path, nil)
	}
	r.Header.Set("Authorization", api.TestAuthHeader())
	return r
}

func mutationRequestBody(t *testing.T, method, path, body string) []byte {
	t.Helper()
	if !requiresOperationID(method, path) {
		return []byte(body)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil || fields == nil {
		return []byte(body)
	}
	if operationID, exists := fields["operation_id"].(string); !exists || strings.TrimSpace(operationID) == "" {
		fields["operation_id"] = uuid.NewString()
	}
	raw, err := json.Marshal(fields)
	testutil.FailErr(t, "encode mutation request", err)
	return raw
}

func requiresOperationID(method, rawPath string) bool {
	path := strings.SplitN(rawPath, "?", 2)[0]
	switch method {
	case http.MethodPut:
		return strings.HasSuffix(path, "/source")
	case http.MethodPost:
		return strings.HasSuffix(path, "/prompts") ||
			strings.HasSuffix(path, "/workflow-runs") ||
			strings.HasSuffix(path, "/source") ||
			strings.HasSuffix(path, "/source/rename") ||
			strings.HasSuffix(path, "/source/copy") ||
			strings.HasSuffix(path, "/search/replace/apply") ||
			strings.HasSuffix(path, "/save")
	default:
		return false
	}
}

func loadMockConfig(t *testing.T) *llm.MockConfig {
	t.Helper()
	cfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}

func createProjectHTTP(t *testing.T, srv *api.Server, projectDir string) wire.Project {
	t.Helper()
	body := fmt.Sprintf(`{"roots":[{"path":%q}]}`, projectDir)
	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", w.Code, w.Body.String())
	}
	var proj wire.Project
	if err := json.Unmarshal(w.Body.Bytes(), &proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	return proj
}

func createSessionWithPostureHTTP(t *testing.T, srv *api.Server, projectDir string, posture wire.SessionPosture) wire.Session {
	t.Helper()
	proj := createProjectHTTP(t, srv, projectDir)
	return createSessionForProjectHTTP(t, srv, proj.ID, posture)
}

// createSessionForProjectHTTP binds a session to an existing project.
func createSessionForProjectHTTP(t *testing.T, srv *api.Server, projectID string, posture wire.SessionPosture) wire.Session {
	t.Helper()
	return createSessionForProjectHandlerHTTP(t, srv, projectID, posture)
}

func createSessionForProjectHandlerHTTP(t *testing.T, serve http.Handler, projectID string, posture wire.SessionPosture) wire.Session {
	t.Helper()
	body := fmt.Sprintf(`{"project_id":%q,"posture":%q}`, projectID, posture)
	req := authedRequest(t, http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	serve.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodPost, "/v1/sessions", nil)
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return waitSessionPreparedHTTP(t, serve, sess)
}

func waitSessionPreparedHTTP(t *testing.T, serve http.Handler, sess wire.Session) wire.Session {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID, nil)
		w := httptest.NewRecorder()
		serve.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("get preparing session status = %d body = %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
			t.Fatalf("decode preparing session: %v", err)
		}
		switch sess.Status {
		case wire.SessionStatusIdle:
			return sess
		case wire.SessionStatusError:
			t.Fatal("session preparation failed")
		case wire.SessionStatusPreparing, wire.SessionStatusBusy:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session preparation did not complete")
	return sess
}

func createSessionHTTP(t *testing.T, srv *api.Server, projectDir string) wire.Session {
	t.Helper()
	return createSessionWithPostureHTTP(t, srv, projectDir, wire.SessionPostureBuild)
}

func exitAmbientRunHTTP(t *testing.T, srv *api.Server, sessionID string) {
	t.Helper()
	req := workflowExitRequest(t, srv, sessionID, "test")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("exit ambient status = %d body = %s", w.Code, w.Body.String())
	}
}

// workflowExitRequest builds the exit request for the session's active run.
func workflowExitRequest(t *testing.T, srv *api.Server, sessionID, reason string) *http.Request {
	t.Helper()
	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sessionID+"/workflow-runs/active", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get active workflow status = %d body = %s", w.Code, w.Body.String())
	}
	run := decodeActiveWorkflowRun(t, w.Body.Bytes())
	if run == nil {
		t.Fatalf("session %s has no active workflow run", sessionID)
	}
	payload := map[string]any{"expected_revision": run.Revision}
	if reason != "" {
		payload["reason"] = reason
	}
	raw, err := json.Marshal(payload)
	testutil.FailErr(t, "encode workflow exit", err)
	exit := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/exit", strings.NewReader(string(raw)))
	exit.Header.Set("Content-Type", "application/json")
	return exit
}

// decodeActiveWorkflowRun reads the active-run envelope; nil means the session has none.
func decodeActiveWorkflowRun(t *testing.T, body []byte) *wire.WorkflowRun {
	t.Helper()
	var active wire.ActiveWorkflowRunResponse
	if err := json.Unmarshal(body, &active); err != nil {
		testutil.FailErr(t, "decode active workflow", err)
	}
	return active.Run
}

// workflowValidationFailure decodes a workflow_validation_failed error body.
type workflowValidationFailure struct {
	Code    wire.ApiErrorCode             `json:"code"`
	Details wire.ComposeValidationDetails `json:"details"`
}

func decodeAPIError(t *testing.T, w *httptest.ResponseRecorder) wire.ErrorResponse {
	t.Helper()
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v body = %s", err, w.Body.String())
	}
	return resp
}

func assertBodyExcludes(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	for _, f := range forbidden {
		if f != "" && strings.Contains(body, f) {
			t.Fatalf("response leaked %q in body: %s", f, body)
		}
	}
}

func acceptPromptHTTP(t *testing.T, srv *api.Server, sessionID, text string) wire.PromptAcceptedResponse {
	t.Helper()
	body := fmt.Sprintf(`{"operation_id":%q,"text":%q}`, uuid.NewString(), text)
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sessionID+"/prompts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d body = %s", w.Code, w.Body.String())
	}
	var accepted wire.PromptAcceptedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode prompt accept: %v", err)
	}
	// Admission may already be claimed when the handler returns.
	switch accepted.Status {
	case "queued", "running":
	default:
		t.Fatalf("prompt accept status = %q, want queued or running", accepted.Status)
	}
	if accepted.OperationID == "" {
		t.Fatal("prompt accept response omitted operation_id")
	}
	return accepted
}

func getSessionHTTP(t *testing.T, srv *api.Server, sessionID string) wire.Session {
	t.Helper()
	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sessionID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get session status = %d body = %s", w.Code, w.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess
}

func listMessagesHTTP(t *testing.T, srv *api.Server, sessionID string) []wire.Message {
	t.Helper()
	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sessionID+"/messages", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list messages status = %d body = %s", w.Code, w.Body.String())
	}
	var page wire.SessionTranscriptPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	msgs := page.Messages
	return msgs
}

const promptIdleBudget = 30 * time.Second

func postPromptAndWaitIdle(t *testing.T, srv *api.Server, sessionID, text string) {
	t.Helper()
	timeout := testutil.Timeout(promptIdleBudget)
	accepted := acceptPromptHTTP(t, srv, sessionID, text)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Two idle reads fence the transcript snapshot.
		preStatus := getSessionHTTP(t, srv, sessionID).Status
		msgs := listMessagesHTTP(t, srv, sessionID)
		postStatus := getSessionHTTP(t, srv, sessionID).Status
		if preStatus == wire.SessionStatusIdle && postStatus == wire.SessionStatusIdle {
			// Receipt identity distinguishes repeated prompts with identical text.
			for _, msg := range msgs {
				if msg.ID == accepted.OperationID && msg.Role == wire.MessageRoleUser {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s prompt %q did not produce a response within %s", sessionID, text, timeout)
}

// postPromptAndWaitTranscriptHTTP waits for matching transcript content.
func postPromptAndWaitTranscriptHTTP(t *testing.T, srv *api.Server, sessionID, text, want string, timeout time.Duration) {
	t.Helper()
	acceptPromptHTTP(t, srv, sessionID, text)
	waitTranscriptContainsHTTP(t, srv, sessionID, want, timeout)
}

func waitTranscriptContainsHTTP(t *testing.T, srv *api.Server, sessionID, want string, timeout time.Duration) {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs := listMessagesHTTP(t, srv, sessionID)
		for _, msg := range msgs {
			if strings.Contains(msg.Content, want) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s transcript did not contain %q within %s; messages: %+v", sessionID, want, timeout, listMessagesHTTP(t, srv, sessionID))
}

func waitTaskEnqueueCountHTTP(t *testing.T, srv *api.Server, sessionID string, want int, timeout time.Duration) {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs := listMessagesHTTP(t, srv, sessionID)
		enqueued := 0
		for _, msg := range msgs {
			if taskMessageEnqueued(msg) {
				enqueued++
			}
		}
		if enqueued == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s task enqueue count != %d within %s; messages: %+v", sessionID, want, timeout, listMessagesHTTP(t, srv, sessionID))
}

func taskMessageEnqueued(message wire.Message) bool {
	return message.Role == wire.MessageRoleTool && message.ToolResult != nil &&
		message.ToolResult.Dispatch != nil && message.ToolResult.Dispatch.WorkerID != ""
}

func waitToolFeedbackHTTP(t *testing.T, srv *api.Server, sessionID, code string, timeout time.Duration) {
	t.Helper()
	testutil.WaitFor(t, timeout, func() bool {
		for _, message := range listMessagesHTTP(t, srv, sessionID) {
			if message.ToolResult == nil {
				continue
			}
			for _, raised := range message.ToolResult.Codes {
				if raised == code {
					return true
				}
			}
		}
		return false
	})
}

func waitWorkflowRunCountHTTP(t *testing.T, srv *api.Server, sessionID string, wantCount int, timeout time.Duration) {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sessionID+"/workflow-runs", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var page wire.WorkflowRunPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if len(page.Runs) == wantCount {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s workflow run count did not reach %d within %s", sessionID, wantCount, timeout)
}

// waitActiveWorkflowRunHTTP waits for an asynchronous workflow start.
func waitActiveWorkflowRunHTTP(t *testing.T, srv *api.Server, sessionID, wantWorkflowID string, timeout time.Duration) wire.WorkflowRun {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sessionID+"/workflow-runs/active", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		last = w.Body.String()
		if w.Code == http.StatusOK {
			if run := decodeActiveWorkflowRun(t, w.Body.Bytes()); run != nil && run.WorkflowID == wantWorkflowID {
				return *run
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s active workflow run %q not present within %s; last=%s", sessionID, wantWorkflowID, timeout, last)
	return wire.WorkflowRun{}
}

const (
	testProviderID    = "test-provider"
	testProviderModel = "test-model"
)

// seedTestProvider creates an assignable local provider.
func seedTestProvider(t *testing.T, base string, requiresAPIKey bool) (string, string) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model"}]}`, testProviderModel)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(backend.Close)

	body := fmt.Sprintf(
		`{"id":%q,"base_url":%q,"requires_api_key":%t,"models":[{"id":%q,"capabilities":{"chat":{"state":"supported"},"streaming":{"state":"unknown"},"tools":{"state":"supported"},"vision":{"state":"unknown"},"reasoning":{"state":"unknown"},"structured_output":{"state":"unknown"},"prompt_caching":{"state":"unknown"}}}]}`,
		testProviderID, backend.URL, requiresAPIKey, testProviderModel,
	)
	openAPIPostJSON[wire.ProviderMeta](t, base, "/v1/providers", nil, body, http.StatusCreated)
	return testProviderID, backend.URL
}

func settingsPut(t *testing.T, base, path, body string) (status int, out []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func settingsPutJSON[T any](t *testing.T, base, path, body string, wantStatus int) T {
	t.Helper()
	status, out := settingsPut(t, base, path, body)
	if status != wantStatus {
		t.Fatalf("PUT %s status = %d, want %d; body = %s", path, status, wantStatus, out)
	}
	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("decode PUT %s: %v body=%s", path, err, out)
	}
	return v
}

func settingsPatch(t *testing.T, base, path, body string) (status int, out []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func settingsPatchJSON[T any](t *testing.T, base, path, body string, wantStatus int) T {
	t.Helper()
	status, out := settingsPatch(t, base, path, body)
	if status != wantStatus {
		t.Fatalf("PATCH %s status = %d, want %d; body = %s", path, status, wantStatus, out)
	}
	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("decode PATCH %s: %v body=%s", path, err, out)
	}
	return v
}
