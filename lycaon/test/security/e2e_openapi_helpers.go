package security

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func createAPIProject(t *testing.T, base, body string) wire.Project {
	t.Helper()
	return openAPIPostJSON[wire.Project](t, base, "/v1/projects", nil, body, http.StatusCreated)
}

func createAPIProjectAtPath(t *testing.T, base, dir string) wire.Project {
	t.Helper()
	return createAPIProject(t, base, `{"roots":[{"path":"`+dir+`"}]}`)
}

// These helpers validate responses before typed decoding.

func interpolatePath(template string, params map[string]string) string {
	out := template
	for k, v := range params {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

type httpCallResult struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func validatedRequest(t *testing.T, base, method, pathTemplate string, pathParams map[string]string, body string) httpCallResult {
	t.Helper()
	url := base + interpolatePath(pathTemplate, pathParams)
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(string(mutationRequestBody(t, method, pathTemplate, body)))
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, rdr)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, pathTemplate, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, pathTemplate, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "read HTTP response body", err)
	return httpCallResult{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       respBody,
	}
}

func openAPIDo(t *testing.T, base, method, pathTemplate string, pathParams map[string]string, body string, wantStatus int) []byte {
	t.Helper()
	res := validatedRequest(t, base, method, pathTemplate, pathParams, body)
	if res.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body = %s", method, pathTemplate, res.StatusCode, wantStatus, res.Body)
	}
	AssertHTTPResponseMatchesOpenAPI(t, res.StatusCode, res.Header, res.Body, method, pathTemplate, pathParams)
	return res.Body
}

func openAPIGetJSON[T any](t *testing.T, base, pathTemplate string, pathParams map[string]string, wantStatus int) T {
	t.Helper()
	respBody := openAPIDo(t, base, http.MethodGet, pathTemplate, pathParams, "", wantStatus)
	return decodeOpenAPI[T](t, http.MethodGet, pathTemplate, respBody)
}

func listMessagesOpenAPI(t *testing.T, base string, pathParams map[string]string, wantStatus int) []wire.Message {
	t.Helper()
	return openAPIGetJSON[wire.SessionTranscriptPage](t, base, "/v1/sessions/{id}/messages", pathParams, wantStatus).Messages
}

func openAPIPostJSON[T any](t *testing.T, base, pathTemplate string, pathParams map[string]string, body string, wantStatus int) T {
	t.Helper()
	respBody := openAPIDo(t, base, http.MethodPost, pathTemplate, pathParams, body, wantStatus)
	if pathTemplate == "/v1/sessions" {
		var sess wire.Session
		if err := json.Unmarshal(respBody, &sess); err != nil {
			t.Fatalf("decode preparing session: %v body=%s", err, respBody)
		}
		waitSessionPreparedOpenAPI(t, base, sess.ID, 10*time.Second)
	}
	return decodeOpenAPI[T](t, http.MethodPost, pathTemplate, respBody)
}

func waitSessionPreparedOpenAPI(t *testing.T, base, sessionID string, timeout time.Duration) {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sess := openAPIGetJSON[wire.Session](t, base, "/v1/sessions/{id}", map[string]string{"id": sessionID}, http.StatusOK)
		if sess.Status != wire.SessionStatusPreparing {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session preparation did not complete")
}

func openAPIPutJSON[T any](t *testing.T, base, pathTemplate string, pathParams map[string]string, body string, wantStatus int) T {
	t.Helper()
	respBody := openAPIDo(t, base, http.MethodPut, pathTemplate, pathParams, body, wantStatus)
	return decodeOpenAPI[T](t, http.MethodPut, pathTemplate, respBody)
}

func openAPIPatchJSON[T any](t *testing.T, base, pathTemplate string, pathParams map[string]string, body string, wantStatus int) T {
	t.Helper()
	respBody := openAPIDo(t, base, http.MethodPatch, pathTemplate, pathParams, body, wantStatus)
	return decodeOpenAPI[T](t, http.MethodPatch, pathTemplate, respBody)
}

func decodeOpenAPI[T any](t *testing.T, method, pathTemplate string, respBody []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(respBody, &v); err != nil {
		t.Fatalf("decode %s %s: %v body=%s", method, pathTemplate, err, respBody)
	}
	return v
}

func acceptPromptOpenAPI(t *testing.T, base, sessionID, body string) {
	t.Helper()
	accepted := openAPIPostJSON[wire.PromptAcceptedResponse](t, base, "/v1/sessions/{id}/prompts",
		map[string]string{"id": sessionID}, body, http.StatusAccepted)
	testutil.WaitFor(t, 10*time.Second, func() bool {
		params := map[string]string{"id": sessionID}
		before := openAPIGetJSON[wire.Session](t, base, "/v1/sessions/{id}", params, http.StatusOK)
		messages := listMessagesOpenAPI(t, base, params, http.StatusOK)
		after := openAPIGetJSON[wire.Session](t, base, "/v1/sessions/{id}", params, http.StatusOK)
		if before.Status != wire.SessionStatusIdle || after.Status != wire.SessionStatusIdle {
			return false
		}
		for _, message := range messages {
			if message.ID == accepted.OperationID && message.Role == wire.MessageRoleUser {
				return true
			}
		}
		return false
	})
}

func waitSessionIdleOpenAPI(t *testing.T, base, sessionID string, timeout time.Duration) wire.Session {
	t.Helper()
	return waitPromptTurnOpenAPI(t, base, sessionID, len(listMessagesOpenAPI(t, base, map[string]string{"id": sessionID}, http.StatusOK)), time.Now(), timeout)
}

func waitPromptTurnOpenAPI(t *testing.T, base, sessionID string, baseline int, acceptedAt time.Time, timeout time.Duration) wire.Session {
	t.Helper()
	timeout = testutil.Timeout(timeout)
	deadline := time.Now().Add(timeout)
	sawBusy := false
	for time.Now().Before(deadline) {
		sess := openAPIGetJSON[wire.Session](t, base, "/v1/sessions/{id}",
			map[string]string{"id": sessionID}, http.StatusOK)
		if sess.Status == wire.SessionStatusBusy {
			sawBusy = true
		}
		msgs := listMessagesOpenAPI(t, base, map[string]string{"id": sessionID}, http.StatusOK)
		if sess.Status == wire.SessionStatusIdle {
			if sawBusy {
				return sess
			}
			for i := baseline; i < len(msgs); i++ {
				if msgs[i].Role != wire.MessageRoleUser {
					return sess
				}
			}
			if time.Since(acceptedAt) >= 150*time.Millisecond {
				return sess
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s did not complete prompt turn within %s", sessionID, timeout)
	return wire.Session{}
}

func lastAssistantStreamURL(t *testing.T, base, sessionID string, timeout time.Duration) (messageID, streamURL string) {
	t.Helper()
	waitSessionIdleOpenAPI(t, base, sessionID, timeout)
	msgs := listMessagesOpenAPI(t, base, map[string]string{"id": sessionID}, http.StatusOK)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == wire.MessageRoleAssistant && strings.TrimSpace(msgs[i].ID) != "" {
			messageID = msgs[i].ID
			streamURL = "/v1/sessions/" + sessionID + "/stream?message=" + messageID
			return messageID, streamURL
		}
	}
	t.Fatalf("no assistant message for session %s", sessionID)
	return "", ""
}
