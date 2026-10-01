package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

func TestSessionFindingsOpenAPI(t *testing.T) {
	h := wiring.BuildForTest(t)
	dir := t.TempDir()
	sess := createSessionHTTP(t, h.Server, dir)

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/findings", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}/findings", map[string]string{"id": sess.ID})
}
