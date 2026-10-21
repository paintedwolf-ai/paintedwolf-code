package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

// The desktop reports stepping away, including the idle step-away of an input
// observer, through one lock route; any other reason is refused.
func TestVaultLockAcceptsStepAwayReasonsOnly(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	for _, reason := range []string{"screen_locked", "sleep", "away", "app_quit"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedRequest(t, http.MethodPost, "/v1/vault/lock", strings.NewReader(`{"reason":"`+reason+`"}`)))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"locked":`) {
			t.Fatalf("%s: status %d body %s", reason, w.Code, w.Body.String())
		}
		AssertResponseMatchesOpenAPI(t, w, http.MethodPost, "/v1/vault/lock", nil)
	}
	for _, reason := range []string{"manual", "idle", "ceiling", "restart", "elsewhere"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedRequest(t, http.MethodPost, "/v1/vault/lock", strings.NewReader(`{"reason":"`+reason+`"}`)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d body %s", reason, w.Code, w.Body.String())
		}
	}
}
