package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

func TestSessionQueueGetOpenAPI(t *testing.T) {
	h := wiring.BuildForTest(t)
	dir := t.TempDir()
	sess := createSessionHTTP(t, h.Server, dir)

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/queue", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}/queue", map[string]string{"id": sess.ID})
}

func TestSessionQueueMutateOpenAPI(t *testing.T) {
	h := wiring.BuildForTest(t)
	dir := t.TempDir()
	sess := createSessionHTTP(t, h.Server, dir)

	req := authedRequest(t, http.MethodPatch, "/v1/sessions/"+sess.ID+"/queue",
		strings.NewReader(`{"op":"hold","hold":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodPatch, "/v1/sessions/{id}/queue", map[string]string{"id": sess.ID})
}

func TestSessionQueueUnknownOpRejected(t *testing.T) {
	h := wiring.BuildForTest(t)
	dir := t.TempDir()
	sess := createSessionHTTP(t, h.Server, dir)

	req := authedRequest(t, http.MethodPatch, "/v1/sessions/"+sess.ID+"/queue",
		strings.NewReader(`{"op":"nonsense"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}
