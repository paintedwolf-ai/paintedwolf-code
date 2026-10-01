package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRateLimitSessionCreate(t *testing.T) {
	t.Setenv("LYCAON_RATE_SESSIONS_PER_MIN", "2")
	t.Setenv("LYCAON_RATE_PROMPTS_PER_MIN", "60")

	srv := newTestServer(t)
	dir := t.TempDir()
	p := createProjectForTest(t, srv, dir)
	body := fmt.Sprintf(`{"project_id":%q,"posture":"build"}`, p.ID)

	for i := 0; i < 2; i++ {
		req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("request %d status = %d body = %s", i+1, w.Code, w.Body.String())
		}
	}

	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assertErrorResponse(t, w, http.StatusTooManyRequests, "rate_limited")
	assertRateLimitRetryContract(t, w)
}

func TestRateLimitPrompts(t *testing.T) {
	t.Setenv("LYCAON_RATE_SESSIONS_PER_MIN", "60")
	t.Setenv("LYCAON_RATE_PROMPTS_PER_MIN", "2")

	dir := t.TempDir()
	srv := newTestServer(t)
	sess := createSessionViaHTTP(t, srv, dir)

	for i := 0; i < 2; i++ {
		req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
			strings.NewReader(promptJSON("hi")))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("prompt %d status = %d body = %s", i+1, w.Code, w.Body.String())
		}
	}

	req := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts",
		strings.NewReader(promptJSON("hi")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assertErrorResponse(t, w, http.StatusTooManyRequests, "rate_limited")
	assertRateLimitRetryContract(t, w)
}

func assertRateLimitRetryContract(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header is missing")
	}
	var body wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode rate limit error: %v", err)
	}
	if !body.Retryable {
		t.Fatalf("retryable = false body=%s", w.Body.String())
	}
}

type sessionIDResp struct {
	ID string `json:"id"`
}

func createSessionViaHTTP(t *testing.T, srv *Server, projectDir string) sessionIDResp {
	t.Helper()
	sess := createSessionAtPathOnServer(t, srv, projectDir, wire.SessionPostureBuild)
	return sessionIDResp{ID: sess.ID}
}

func TestNavigationCannotStarveEditingAndCleanup(t *testing.T) {
	srv := newTestServer(t)
	// Spend every navigation and general-write token without involving handlers.
	srv.rateLimits.source.AllowN(time.Now(), srv.rateLimits.source.Burst())
	srv.rateLimits.writes.AllowN(time.Now(), srv.rateLimits.writes.Burst())
	for _, operation := range []generatedOperation{operationSyncEditorDocument, operationPublishEditorDocumentPresence, operationLeaveEditorDocument, operationReleaseSourceView, operationReleaseSourcePresentation, operationReleaseSourceViewportInterest} {
		t.Run(operation.ID, func(t *testing.T) {
			handler := srv.rateLimitOperation(operation, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(operation.Method, operation.Path, nil))
			if w.Code != http.StatusNoContent {
				t.Fatalf("unrelated activity blocked %s: %d", operation.ID, w.Code)
			}
		})
	}
	w := httptest.NewRecorder()
	srv.rateLimitOperation(operationCreateSourceView, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("navigation ignored its own budget") })).ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	assertErrorResponse(t, w, http.StatusTooManyRequests, "rate_limited")
	assertRateLimitRetryContract(t, w)
}
