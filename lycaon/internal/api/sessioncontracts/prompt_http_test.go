package sessioncontracts

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptReturnsAccepted(t *testing.T) {
	dir := t.TempDir()
	srv := contractfixture.NewTestServer(t)

	sess := contractfixture.CreateSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(contractfixture.PromptJSON("hello")))
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
	for _, Text := range []string{
		"Document {{paintedwolf-secret:example}} and the prefix {{paintedwolf-secret:",
		"Explain {{paintedwolf-secret:123e4567-e89b-42d3-a456-426614174000}}",
	} {
		srv := contractfixture.NewTestServer(t)
		sess := contractfixture.CreateSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(contractfixture.PromptJSON(Text))))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("literal prompt status = %d body = %s", rec.Code, rec.Body.String())
		}
	}
}

func TestPromptReplayPrecedesWorkspaceValidation(t *testing.T) {
	dir := t.TempDir()
	srv := contractfixture.NewTestServer(t)
	sess := contractfixture.CreateSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)
	operationID := uuid.NewString()
	prompt := wire.PromptRequest{OperationID: operationID, Text: "hello"}
	row, created, err := srv.Admin.SessionAdmin.Lifecycle.Sessions.Submissions.AdmitPrompt(t.Context(), sess.ID, operationID, prompt, promptinput.Input{Text: prompt.Text})
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
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(string(body)))
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
	req = contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPromptEmptyText(t *testing.T) {
	dir := t.TempDir()
	srv := contractfixture.NewTestServer(t)

	sess := contractfixture.CreateSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(contractfixture.PromptJSON("   ")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPromptEmptyTextAcceptedByAmbientWorkflowRequest(t *testing.T) {
	dir := t.TempDir()
	srv := contractfixture.NewTestServerWithWorkflows(t)
	sess := contractfixture.CreateSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(contractfixture.PromptJSON("   ")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPromptRejectsInlineTextPastComposerLimit(t *testing.T) {
	dir := t.TempDir()
	srv := contractfixture.NewTestServer(t)
	sess := contractfixture.CreateSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)
	Text := strings.Repeat("x", srv.Admin.Prompt.Submission.Caps.Composer.MaxInlineText.Int()+1)

	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(contractfixture.PromptJSON(Text)))
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
	srv := contractfixture.NewTestServer(t)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/nope/prompts",
		strings.NewReader(contractfixture.PromptJSON("hello")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestStreamUnknownMessage(t *testing.T) {
	dir := t.TempDir()
	srv := contractfixture.NewTestServer(t)

	sess := contractfixture.CreateSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/stream?message=bad-id", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUnavailableToolFixtureDoesNotExecuteHTTP(t *testing.T) {
	reg := tools.NewStubRegistry()
	reg.SetFail("read", fmt.Errorf("read failed"))
	srv := contractfixture.NewTestServerWithWorkflowRegistry(t, contractfixture.WithoutTools(reg, "read"))
	baseURL := contractfixture.StartTestHTTPServer(t, srv)
	dir := t.TempDir()

	sess := contractfixture.CreateTestSession(t, baseURL, dir)
	contractfixture.AcceptPrompt(t, baseURL, sess.ID, "read the readme")
	_, streamURL := contractfixture.WaitForAssistantStream(t, baseURL, sess.ID, 10*time.Second)

	content, sawDone := contractfixture.ReadSSEStream(t, baseURL+streamURL)
	if !sawDone {
		t.Fatal("expected done chunk")
	}
	if content != "I understand. How can I help you further?" {
		t.Fatalf("stream content = %q", content)
	}
}

func TestPromptConcurrentHTTP(t *testing.T) {
	dir := t.TempDir()
	srv := contractfixture.NewTestServer(t)
	baseURL := contractfixture.StartTestHTTPServer(t, srv)
	sess := contractfixture.CreateTestSession(t, baseURL, dir)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, Text := range []string{"hello one", "hello two"} {
		wg.Add(1)
		go func(Text string) {
			defer wg.Done()
			resp, err := contractfixture.AuthedHTTPPost(baseURL+"/v1/sessions/"+sess.ID+"/prompts",
				"application/json", contractfixture.PromptJSON(Text))
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				errs <- fmt.Errorf("status %d for %q", resp.StatusCode, Text)
			}
		}(Text)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "operation failed", err)
	}
}

func TestPromptMaxIterationHTTP(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	srv.Admin.SessionAdmin.Lifecycle.Sessions.Limits.SetMaxIterations(3)
	baseURL := contractfixture.StartTestHTTPServer(t, srv)
	dir := t.TempDir()
	sess := contractfixture.CreateTestSession(t, baseURL, dir)

	contractfixture.AcceptPrompt(t, baseURL, sess.ID, "infinite loop")
	contractfixture.WaitForSessionIdle(t, baseURL, sess.ID, 10*time.Second)
	msgs := contractfixture.ListMessagesAtURL(t, baseURL, sess.ID)
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
	srv := contractfixture.NewTestServerWithWorkflows(t)
	baseURL := contractfixture.StartTestHTTPServer(t, srv)
	dir := t.TempDir()
	sess := contractfixture.CreateTestSession(t, baseURL, dir)

	resp, err := contractfixture.AuthedHTTPGet(baseURL + "/v1/sessions/" + sess.ID)
	testutil.FailErr(t, "authedHTTPGet failed", err)
	var before wire.Session
	json.NewDecoder(resp.Body).Decode(&before)
	resp.Body.Close()
	if before.Status != wire.SessionStatusIdle {
		t.Fatalf("before status = %q", before.Status)
	}

	contractfixture.AcceptPrompt(t, baseURL, sess.ID, "hello")
	after := contractfixture.WaitForSessionIdle(t, baseURL, sess.ID, 10*time.Second)
	if after.Status != wire.SessionStatusIdle {
		t.Fatalf("after status = %q", after.Status)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatal("expected updated_at to advance after prompt")
	}
}
