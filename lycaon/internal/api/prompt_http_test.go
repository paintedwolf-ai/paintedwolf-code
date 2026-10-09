package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptReturnsAccepted(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)

	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(promptJSON("hello")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var accepted wire.PromptAcceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if accepted.Status != "queued" {
		t.Fatalf("status = %q", accepted.Status)
	}
}

func TestPromptAcceptsLiteralSecretReferenceText(t *testing.T) {
	for _, text := range []string{
		"Document {{paintedwolf-secret:example}} and the prefix {{paintedwolf-secret:",
		"Explain {{paintedwolf-secret:123e4567-e89b-42d3-a456-426614174000}}",
	} {
		srv := newTestServer(t)
		sess := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(promptJSON(text))))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("literal prompt status = %d body = %s", rec.Code, rec.Body.String())
		}
	}
}

func TestPromptReplayPrecedesWorkspaceValidation(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)
	operationID := uuid.NewString()
	prompt := wire.PromptRequest{OperationID: operationID, Text: "hello"}
	row, created, err := srv.sessions.Submissions.AdmitPrompt(t.Context(), sess.ID, operationID, prompt, promptinput.Input{Text: prompt.Text})
	if err != nil {
		testutil.FailErr(t, "admit prompt", err)
	}
	if !created {
		t.Fatal("expected a new prompt receipt")
	}
	if err := os.Rename(dir, dir+"-moved"); err != nil {
		testutil.FailErr(t, "move workspace", err)
	}

	body, err := json.Marshal(prompt)
	if err != nil {
		testutil.FailErr(t, "marshal prompt", err)
	}
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var accepted wire.PromptAcceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		testutil.FailErr(t, "unmarshal accepted prompt", err)
	}
	if accepted.OperationID != row.ID {
		t.Fatalf("operation_id = %q, want %q", accepted.OperationID, row.ID)
	}

	prompt.Text = "different"
	body, err = json.Marshal(prompt)
	if err != nil {
		testutil.FailErr(t, "marshal conflicting prompt", err)
	}
	req = newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPromptEmptyText(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)

	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(promptJSON("   ")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPromptEmptyTextAcceptedByAmbientWorkflowRequest(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithWorkflows(t)
	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(promptJSON("   ")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPromptRejectsInlineTextPastComposerLimit(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)
	text := strings.Repeat("x", srv.Prompt.Caps.Composer.MaxInlineText.Int()+1)

	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(promptJSON(text)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"prompt_text_too_large"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPromptUnknownSession(t *testing.T) {
	srv := newTestServer(t)
	req := newAuthedRequest(http.MethodPost, "/v1/sessions/nope/prompts",
		strings.NewReader(promptJSON("hello")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestStreamUnknownMessage(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)

	sess := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	req := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/stream?message=bad-id", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUnavailableToolFixtureDoesNotExecuteHTTP(t *testing.T) {
	reg := tools.NewStubRegistry()
	reg.SetFail("read", fmt.Errorf("read failed"))
	srv := newTestServerWithWorkflowRegistry(t, withoutTools(reg, "read"))
	baseURL := startTestHTTPServer(t, srv)
	dir := t.TempDir()

	sess := createTestSession(t, baseURL, dir)
	acceptPrompt(t, baseURL, sess.ID, "read the readme")
	_, streamURL := waitForAssistantStream(t, baseURL, sess.ID, 10*time.Second)

	content, sawDone := readSSEStream(t, baseURL+streamURL)
	if !sawDone {
		t.Fatal("expected done chunk")
	}
	if content != "I understand. How can I help you further?" {
		t.Fatalf("stream content = %q", content)
	}
}

func TestPromptConcurrentHTTP(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	baseURL := startTestHTTPServer(t, srv)
	sess := createTestSession(t, baseURL, dir)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, text := range []string{"hello one", "hello two"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			resp, err := authedHTTPPost(baseURL+"/v1/sessions/"+sess.ID+"/prompts",
				"application/json", promptJSON(text))
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				errs <- fmt.Errorf("status %d for %q", resp.StatusCode, text)
			}
		}(text)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "operation failed", err)
	}
}

func TestPromptMaxIterationHTTP(t *testing.T) {
	srv := newTestServer(t)
	srv.sessions.Limits.SetMaxIterations(3)
	baseURL := startTestHTTPServer(t, srv)
	dir := t.TempDir()
	sess := createTestSession(t, baseURL, dir)

	acceptPrompt(t, baseURL, sess.ID, "infinite loop")
	waitForSessionIdle(t, baseURL, sess.ID, 10*time.Second)
	msgs := listMessagesAtURL(t, baseURL, sess.ID)
	var lastAssistant *wire.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == wire.MessageRoleAssistant {
			lastAssistant = &msgs[i]
			break
		}
	}
	if lastAssistant == nil || len(lastAssistant.ToolCalls) != 0 {
		t.Fatalf("expected prose-only finish assistant after iteration cap, got %+v", lastAssistant)
	}
}

func TestPromptSessionStatusLifecycle(t *testing.T) {
	// The lifecycle assertions need a build session that reaches idle, so ambient
	// attach has to succeed.
	srv := newTestServerWithWorkflows(t)
	baseURL := startTestHTTPServer(t, srv)
	dir := t.TempDir()
	sess := createTestSession(t, baseURL, dir)

	resp, err := authedHTTPGet(baseURL + "/v1/sessions/" + sess.ID)
	testutil.FailErr(t, "authedHTTPGet failed", err)
	var before wire.Session
	json.NewDecoder(resp.Body).Decode(&before)
	resp.Body.Close()
	if before.Status != wire.SessionStatusIdle {
		t.Fatalf("before status = %q", before.Status)
	}

	acceptPrompt(t, baseURL, sess.ID, "hello")
	after := waitForSessionIdle(t, baseURL, sess.ID, 10*time.Second)
	if after.Status != wire.SessionStatusIdle {
		t.Fatalf("after status = %q", after.Status)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatal("expected updated_at to advance after prompt")
	}
}
