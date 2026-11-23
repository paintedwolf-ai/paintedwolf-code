package httpaction

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func issuerServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"issued-token-4411"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func relyingServer(t *testing.T, seen *string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server
}

func captureIssuedToken(t *testing.T, deps Deps, issuer *httptest.Server, root string) {
	t.Helper()
	_, err := runRequest(t, deps, map[string]any{
		"url": issuer.URL + "/login", "token_jar": "jar",
		"capture_tokens":     []any{map[string]any{"name": "api", "from": "json:access_token"}},
		"capability_request": loopbackCapability(t, issuer.URL),
	}, sessionContext(root, "login"))
	testutil.FailErr(t, "capture token", err)
}

func TestTokenIssuedElsewhereIsReviewedBeforePlacement(t *testing.T) {
	for name, tc := range map[string]struct {
		decision   secretmatch.Decision
		wantCode   string
		wantOnWire string
	}{
		"withheld":       {decision: secretmatch.Withhold, wantCode: toolrejection.OutboundSecretDeniedCode},
		"sent unchanged": {decision: secretmatch.SendUnchanged, wantOnWire: "Bearer issued-token-4411"},
		"sent redacted":  {decision: secretmatch.SendRedacted, wantOnWire: "Bearer " + secretmatch.RedactedMarker},
	} {
		t.Run(name, func(t *testing.T) {
			secrets, _ := testSecrets(t)
			issuer := issuerServer(t)
			var seen string
			relying := relyingServer(t, &seen)
			var asked []secretmatch.Alert
			deps := Deps{Boundary: testBoundary(), Secrets: secrets, Jars: secrets.Jars, SecretMatcher: testSecretMatcher(t),
				SecretAsk: func(_ context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
					asked = append(asked, alert)
					return secretmatch.Resolution{Decision: tc.decision, ReceiptToken: "receipt-1"}, nil
				}}
			root := t.TempDir()
			captureIssuedToken(t, deps, issuer, root)
			if len(asked) != 0 {
				t.Fatalf("capturing a token asked %d times", len(asked))
			}

			_, err := runRequestWith(t, deps, map[string]any{
				"url": relying.URL + "/api", "token_jar": "jar",
				"headers":            []any{map[string]any{"name": "Authorization", "value": "Bearer {{token:api}}"}},
				"capability_request": loopbackCapability(t, relying.URL),
			}, sessionContext(root, "relying"), tokenHeaderOptions())

			if len(asked) != 1 {
				t.Fatalf("asked %d times, want one review of the disclosure", len(asked))
			}
			alert := asked[0]
			if alert.Surface != secretmatch.SurfaceHTTPRequest || !strings.HasPrefix(alert.DestinationLabel, "http://127.0.0.1:") ||
				strings.Contains(alert.DestinationLabel, strings.TrimPrefix(issuer.URL, "http://")) {
				t.Fatalf("alert destination = %q, want the relying service", alert.DestinationLabel)
			}
			if alert.RuleID != secretmatch.ManagedRuleID || len(alert.SecretNames) != 1 || alert.SecretNames[0] != "api" || alert.Container != "token jar jar" {
				t.Fatalf("alert = %+v, want managed evidence naming the token", alert)
			}
			if len(alert.Fingerprints) != 1 || alert.ReviewValue != "" {
				t.Fatalf("alert fingerprints = %v review value = %q, want one fingerprint and no value", alert.Fingerprints, alert.ReviewValue)
			}
			if tc.wantCode != "" {
				var reject *toolrejection.ToolReject
				if !errors.As(err, &reject) || reject.Code != tc.wantCode {
					t.Fatalf("err = %v, want %s", err, tc.wantCode)
				}
				if reject.Data["host"] == "" || reject.Data["tokens"] == nil {
					t.Fatalf("reject data = %v, want the host and the withheld token names", reject.Data)
				}
				if seen != "" {
					t.Fatalf("the relying service received %q", seen)
				}
				return
			}
			testutil.FailErr(t, "relying request", err)
			if seen != tc.wantOnWire {
				t.Fatalf("relying service received %q, want %q", seen, tc.wantOnWire)
			}
		})
	}
}

func TestTokenReturnsToItsIssuerWithoutReview(t *testing.T) {
	secrets, _ := testSecrets(t)
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"issued-token-4411"}`))
			return
		}
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	deps := Deps{Boundary: testBoundary(), Secrets: secrets, Jars: secrets.Jars, SecretMatcher: testSecretMatcher(t),
		SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			t.Fatal("a same-service placement was reviewed")
			return secretmatch.Resolution{}, nil
		}}
	root := t.TempDir()
	captureIssuedToken(t, deps, server, root)
	_, err := runRequestWith(t, deps, map[string]any{
		"url": server.URL + "/api", "token_jar": "jar",
		"headers":            []any{map[string]any{"name": "Authorization", "value": "Bearer {{token:api}}"}},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "api"), tokenHeaderOptions())
	testutil.FailErr(t, "same-service request", err)
	if seen != "Bearer issued-token-4411" {
		t.Fatalf("issuer received %q", seen)
	}
}

func TestTokenIssuedElsewhereWithoutAScreenIsAFault(t *testing.T) {
	secrets, _ := testSecrets(t)
	issuer := issuerServer(t)
	var seen string
	relying := relyingServer(t, &seen)
	deps := Deps{Boundary: testBoundary(), Secrets: secrets, Jars: secrets.Jars}
	root := t.TempDir()
	captureIssuedToken(t, deps, issuer, root)
	_, err := runRequestWith(t, deps, map[string]any{
		"url": relying.URL + "/api", "token_jar": "jar",
		"headers":            []any{map[string]any{"name": "Authorization", "value": "Bearer {{token:api}}"}},
		"capability_request": loopbackCapability(t, relying.URL),
	}, sessionContext(root, "relying"), tokenHeaderOptions())
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != toolrejection.OutboundSecretScreenFailedCode {
		t.Fatalf("err = %v, want %s", err, toolrejection.OutboundSecretScreenFailedCode)
	}
	if seen != "" {
		t.Fatalf("the relying service received %q", seen)
	}
}

func tokenHeaderOptions() ExchangeOptions {
	return ExchangeOptions{Output: &JarOutput{}, Slots: JarSlots{Headers: []outboundhttp.Header{{Name: "Authorization", Value: "Bearer {{token:api}}"}}}}
}
