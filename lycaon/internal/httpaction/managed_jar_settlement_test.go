package httpaction

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestCompletedHTTPMutationSurvivesCookiePersistenceFailure(t *testing.T) {
	secrets, database := testSecretsWithStore(t)
	var orders atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		orders.Add(1)
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "fixture-session", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		if err := database.Close(); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	args := map[string]any{"method": "POST", "url": server.URL, "cookie_jar": "orders", "capability_request": loopbackCapability(t, server.URL)}
	_, err := Perform(t.Context(), Deps{Boundary: testBoundary(), Secrets: secrets, Jars: secrets.Jars}, args, sessionContext(t.TempDir(), "create-order"), ExchangeOptions{Output: &JarOutput{}})
	var exchange *outboundhttp.ExchangeError
	reject := toolrejection.AsToolReject(err)
	if orders.Load() != 1 || !errors.As(err, &exchange) || exchange.RequestsStarted != 1 || exchange.ResponsesReceived != 1 || exchange.LastStatus != http.StatusCreated {
		t.Fatalf("completed service operation lost its exchange facts: %v", err)
	}
	if reject == nil || reject.Code != CookieJarFailedCode || reject.Data["persisted"] != false || reject.Data["cookie_jar_name"] != "orders" {
		t.Fatalf("missing failed persistence receipt: %v", err)
	}
}
