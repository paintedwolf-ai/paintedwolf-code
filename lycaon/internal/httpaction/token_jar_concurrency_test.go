package httpaction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A request that captures nothing leaves the stored jar as it is, so a token
// another request captured meanwhile survives.
func TestUncapturingRequestKeepsConcurrentCaptures(t *testing.T) {
	service, _ := managedRequestService(t)
	deps := Deps{Boundary: testBoundary(), Secrets: service, SecretMatcher: testSecretMatcher(t)}
	issuer := issuerServer(t)
	root := t.TempDir()
	captureIssuedToken(t, deps, issuer, root)
	jarReq := secretcap.TokenJarRequest{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "other", OperationID: "other-op", Name: "jar"}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer plain.Close()
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		other, err := service.OpenTokenJar(context.Background(), jarReq)
		testutil.FailErr(t, "open jar concurrently", err)
		other.Set("late", secretcap.Token{Value: "late-token-7731", Origin: "late-origin", OriginLabel: "late.example"})
		_, err = service.SaveTokenJar(context.Background(), jarReq, other)
		testutil.FailErr(t, "save concurrent capture", err)
		return true
	})
	_, err := runRequest(t, deps, map[string]any{
		"url": plain.URL + "/ping", "token_jar": "jar", "capability_request": loopbackCapability(t, plain.URL),
	}, sessionContext(root, "uncapturing"))
	testutil.FailErr(t, "uncapturing request", err)

	confine.SetEgressResolver(nil)
	after, err := service.OpenTokenJar(t.Context(), jarReq)
	testutil.FailErr(t, "reopen jar", err)
	if _, ok := after.Tokens["late"]; !ok {
		t.Fatalf("a request that captured nothing dropped a concurrent capture: %v", after.Tokens)
	}
	if _, ok := after.Tokens["api"]; !ok {
		t.Fatalf("the original token is gone: %v", after.Tokens)
	}
}

// A reference is placed only in headers and the body. One in the URL or
// host_header is refused before anything is sent, not sent as literal text.
func TestReferencesOutsideHeadersAndBodyAreRefused(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	deps := Deps{Boundary: testBoundary()}
	for name, args := range map[string]map[string]any{
		"token in query": {"url": server.URL + "/x?key={{token:api}}"},
		"cookie in path": {"url": server.URL + "/{{cookie:session}}/x"},
		"token in host":  {"url": server.URL + "/x", "host_header": "{{token:api}}.example.test"},
	} {
		args["capability_request"] = loopbackCapability(t, server.URL)
		_, err := runRequest(t, deps, args, sessionContext(t.TempDir(), "ref-"+name))
		if err == nil {
			t.Errorf("%s: request was sent", name)
		}
	}
	if hits != 0 {
		t.Fatalf("a request carrying a misplaced reference reached the server %d times", hits)
	}
}
