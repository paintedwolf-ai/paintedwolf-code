package contractfixture

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// fixtureClient reaches loopback test servers; its bound turns a hung handler
// into a test failure instead of a stalled run.
var fixtureClient = &http.Client{Timeout: 2 * time.Minute}

func AcceptPrompt(t *testing.T, baseURL, sessionID, text string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/sessions/"+sessionID+"/prompts",
		strings.NewReader(PromptJSON(text)))
	if err != nil {
		t.Fatalf("prompt request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := fixtureClient.Do(req)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt status = %d body = %s", resp.StatusCode, string(ReadBody(t, resp)))
	}
	var accepted wire.PromptAcceptedResponse
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode prompt accept: %v", err)
	}
	if accepted.Status != "queued" {
		t.Fatalf("prompt accept status = %q", accepted.Status)
	}
	WaitForSessionIdle(t, baseURL, sessionID, 10*time.Second)
}

func AssertErrorResponse(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantCode string) {
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

func AuthedHTTPGet(url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	hostapi.WithTestAuth(req)
	return fixtureClient.Do(req)
}

// testDeps adjusts the dependencies a test server is built with.

func AuthedHTTPPost(url, contentType, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	hostapi.WithTestAuth(req)
	return fixtureClient.Do(req)
}

func CreateProjectForTest(t *testing.T, srv *hostapi.Server, dir string) wire.Project {
	t.Helper()
	var resp wire.Project
	DecodeCreateProjectForTest(t, srv, dir, &resp)
	return resp
}

func CreateSessionAtPathOnServer(t *testing.T, srv *hostapi.Server, dir string, posture wire.SessionPosture) wire.Session {
	t.Helper()
	p := CreateProjectForTest(t, srv, dir)
	body := fmt.Sprintf(`{"project_id":%q,"posture":%q}`, p.ID, posture)
	req := NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body=%s", w.Code, w.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	DrainBackground(t, srv)
	sess.Status = wire.SessionStatusIdle
	return sess
}

func CreateTestSession(t *testing.T, baseURL, projectDir string) wire.Session {
	t.Helper()
	projBody := fmt.Sprintf(`{"roots":[{"path":%q}]}`, projectDir)
	projResp, err := AuthedHTTPPost(baseURL+"/v1/projects", "application/json", projBody)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	defer func() { _ = projResp.Body.Close() }()
	if projResp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", projResp.StatusCode, string(ReadBody(t, projResp)))
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
	hostapi.WithTestAuth(req)
	resp, err := fixtureClient.Do(req)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", resp.StatusCode, string(ReadBody(t, resp)))
	}
	var sess wire.Session
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return WaitSessionPrepared(t, baseURL, sess)
}

// waitSessionPrepared polls a newly created session until preparation ends;
// creation answers 202 while the workspace is still being prepared.

func DecodeCreateProjectForTest(t *testing.T, srv *hostapi.Server, dir string, out *wire.Project) {
	t.Helper()
	body := fmt.Sprintf(`{"roots":[{"path":%q}]}`, dir)
	req := NewAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("decode create project response: %v", err)
	}
}

func GetSessionAtURL(t *testing.T, baseURL, sessionID string) wire.Session {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID, nil)
	if err != nil {
		t.Fatalf("get session request: %v", err)
	}
	hostapi.WithTestAuth(req)
	resp, err := fixtureClient.Do(req)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := ReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d body = %s", resp.StatusCode, body)
	}
	var sess wire.Session
	if err := json.Unmarshal(body, &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess
}

func ListMessagesAtURL(t *testing.T, baseURL, sessionID string) []wire.Message {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/messages", nil)
	if err != nil {
		t.Fatalf("list messages request: %v", err)
	}
	hostapi.WithTestAuth(req)
	resp, err := fixtureClient.Do(req)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list messages status = %d body = %s", resp.StatusCode, string(ReadBody(t, resp)))
	}
	var page wire.SessionTranscriptPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	return page.Messages
}

func NewAuthedRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", httpio.MediaTypeJSON)
	}
	if strings.HasPrefix(target, "/v1") {
		hostapi.WithTestAuth(req)
	}
	return req
}

func PromptJSON(text string) string {
	return fmt.Sprintf(`{"operation_id":%q,"text":%q}`, uuid.NewString(), text)
}

// waitForSessionIdle waits until no admitted prompt is pending and no turn runs;
// prompt_pending covers the gap between admission and the turn going busy.

func ReadSSEStream(t *testing.T, streamURL string) (content string, sawDone bool) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	hostapi.WithTestAuth(req)
	resp, err := fixtureClient.Do(req)
	if err != nil {
		t.Fatalf("stream get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
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

func StartTestHTTPServer(t *testing.T, srv *hostapi.Server) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpServer := &http.Server{Handler: srv, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	return fmt.Sprintf("http://%s", listener.Addr().String())
}

func WaitForAssistantStream(t *testing.T, baseURL, sessionID string, timeout time.Duration) (messageID, streamURL string) {
	t.Helper()
	WaitForSessionIdle(t, baseURL, sessionID, timeout)
	msgs := ListMessagesAtURL(t, baseURL, sessionID)
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

func WaitForSessionIdle(t *testing.T, baseURL, sessionID string, timeout time.Duration) wire.Session {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sess := GetSessionAtURL(t, baseURL, sessionID)
		if sess.Status == wire.SessionStatusIdle && !sess.PromptPending {
			return sess
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session %s prompt turn did not complete within %s", sessionID, timeout)
	return wire.Session{}
}

func WaitSessionPrepared(t *testing.T, baseURL string, sess wire.Session) wire.Session {
	t.Helper()
	deadline := time.Now().Add(testutil.Timeout(10 * time.Second))
	for sess.Status == wire.SessionStatusPreparing && time.Now().Before(deadline) {
		readyResp, getErr := AuthedHTTPGet(baseURL + "/v1/sessions/" + sess.ID)
		if getErr != nil {
			t.Fatalf("wait for session preparation: %v", getErr)
		}
		if readyResp.StatusCode != http.StatusOK {
			_ = readyResp.Body.Close()
			t.Fatalf("wait for session preparation status = %d", readyResp.StatusCode)
		}
		if DecodeErr := json.NewDecoder(readyResp.Body).Decode(&sess); DecodeErr != nil {
			_ = readyResp.Body.Close()
			t.Fatalf("decode prepared session: %v", DecodeErr)
		}
		_ = readyResp.Body.Close()
		if sess.Status == wire.SessionStatusPreparing {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if sess.Status == wire.SessionStatusPreparing {
		t.Fatal("session preparation did not complete")
	}
	return sess
}
