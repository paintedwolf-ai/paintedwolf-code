package security

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestSessionUnknownIDReturnsNotFound(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	sess := createSessionHTTP(t, srv, dir)

	unknownID := uuid.New().String()
	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/sessions/" + unknownID, ""},
		{http.MethodPost, "/v1/sessions/" + unknownID + "/prompts", `{"text":"probe"}`},
		{http.MethodGet, "/v1/sessions/" + unknownID + "/stream?message=00000000-0000-4000-8000-000000000001", ""},
		{http.MethodPost, "/v1/sessions/" + unknownID + "/compact", ""},
		{http.MethodGet, "/v1/sessions/" + unknownID + "/context", ""},
		{http.MethodGet, "/v1/sessions/" + unknownID + "/findings", ""},
		{http.MethodGet, "/v1/sessions/" + unknownID + "/progress", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			var reqBody io.Reader
			if ep.body != "" {
				reqBody = strings.NewReader(ep.body)
			}
			req := authedRequest(t, ep.method, ep.path, reqBody)
			if ep.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
			}
			errResp := decodeAPIError(t, w)
			if errResp.Code != "session_not_found" && errResp.Code != "message_not_found" {
				t.Fatalf("code = %q, want session_not_found or message_not_found", errResp.Code)
			}
			assertBodyExcludes(t, w.Body.String(), sess.WorkspacePath, sess.ID, "sk-", "api_key")
		})
	}
}

func TestSessionKnownIDAccessible(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	sess := createSessionHTTP(t, srv, dir)

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}", map[string]string{"id": sess.ID})
	if !strings.Contains(w.Body.String(), sess.ID) {
		t.Fatalf("expected session id in response")
	}
}
