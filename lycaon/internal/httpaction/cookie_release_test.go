package httpaction

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// A cookie whose receipt names a retained service object is foreign to every
// other recipient. When its disclosure cannot be decided, the request must stop
// before any byte leaves and the protected jar must stay as it was.
func TestForeignIssuerCookieFaultsStopTheRequestBeforeDelivery(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "cookie matcher", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("f", 32)))
	testutil.FailErr(t, "cookie fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)

	cases := []struct {
		name    string
		matcher *secretmatch.Matcher
		ask     func(*atomic.Int64) secretmatch.AskFunc
		asked   int64
		stage   string
	}{
		{name: "disclosure_unwired", matcher: matcher, stage: secretmatch.FaultStageScreenUnwired},
		{name: "matcher_unavailable", ask: answering(nil), stage: secretmatch.FaultStageScreenUnwired},
		{name: "disclosure_error", matcher: matcher, ask: answering(errors.New("card store offline")), asked: 1, stage: secretmatch.FaultStageRaise},
		{name: "disclosure_fault", matcher: matcher, ask: answering(secretmatch.NewAskFault(secretmatch.FaultStageNoSession, nil)), asked: 1, stage: secretmatch.FaultStageNoSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secrets, _ := testSecrets(t)
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			before := seedRetainedIssuerCookie(t, secrets, server.URL)

			var asked atomic.Int64
			deps := Deps{Boundary: testBoundary(), Secrets: secrets, SecretMatcher: tc.matcher}
			if tc.ask != nil {
				deps.SecretAsk = tc.ask(&asked)
			}
			_, err := runRequest(t, deps, map[string]any{
				"url": server.URL + "/private", "cookie_jar": "service",
				"capability_request": loopbackCapability(t, server.URL),
			}, sessionContext(t.TempDir(), "ordinary-transport"))
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != toolrejection.OutboundSecretScreenFailedCode || reject.Data["fault_stage"] != tc.stage {
				t.Fatalf("error = %v, want %s at %s", err, toolrejection.OutboundSecretScreenFailedCode, tc.stage)
			}
			if requests.Load() != 0 || asked.Load() != tc.asked {
				t.Fatalf("requests=%d asks=%d, want no delivery and %d asks", requests.Load(), asked.Load(), tc.asked)
			}
			after, err := secrets.OpenCookieJar(t.Context(), cookieSeedRequest("inspect"))
			testutil.FailErr(t, "reopen protected jar", err)
			if !reflect.DeepEqual(after.Store.Snapshot(), before) {
				t.Fatal("a disclosure fault changed the protected cookie")
			}
		})
	}
}

func answering(err error) func(*atomic.Int64) secretmatch.AskFunc {
	return func(asked *atomic.Int64) secretmatch.AskFunc {
		return func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			asked.Add(1)
			return secretmatch.Resolution{}, err
		}
	}
}

func cookieSeedRequest(operation string) secretcap.CookieJarRequest {
	return secretcap.CookieJarRequest{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: operation, Name: "service"}
}

// seedRetainedIssuerCookie stores a cookie as an accepted response over a
// retained socket object writes it, and returns its persisted form.
func seedRetainedIssuerCookie(t *testing.T, secrets *secretcap.Service, rawURL string) []httpcookies.Cookie {
	t.Helper()
	jar, err := secrets.OpenCookieJar(t.Context(), cookieSeedRequest("seed"))
	testutil.FailErr(t, "open seed jar", err)
	target, err := url.Parse(rawURL + "/private")
	testutil.FailErr(t, "seed target", err)
	jar.Store.SetDelivery("retained-service-binding", nil)
	jar.Store.SetCookies(target, []*http.Cookie{{Name: "session", Value: "retained-service-session-4411", Path: "/"}})
	_, err = secrets.SaveCookieJar(t.Context(), cookieSeedRequest("seed"), jar)
	testutil.FailErr(t, "persist seed jar", err)
	stored, err := secrets.OpenCookieJar(t.Context(), cookieSeedRequest("inspect"))
	testutil.FailErr(t, "reopen seed jar", err)
	snapshot := stored.Store.Snapshot()
	if len(snapshot) != 1 || snapshot[0].NativeIssuer == "" {
		t.Fatalf("seeded cookie lacks its issuer receipt: %+v", snapshot)
	}
	return snapshot
}

func TestCookieRecipientOverridesRequireReview(t *testing.T) {
	target, err := url.Parse("https://issuer.example/api")
	testutil.FailErr(t, "parse target", err)
	plain := requestSpec{target: target, method: http.MethodGet}

	for name, tc := range map[string]struct {
		spec    requestSpec
		foreign bool
	}{
		"plain":              {plain, false},
		"matching Host":      {requestSpec{target: target, method: http.MethodGet, hostHeader: "ISSUER.example:443"}, false},
		"different Host":     {requestSpec{target: target, method: http.MethodGet, hostHeader: "other.example"}, true},
		"applicable mapping": {requestSpec{target: target, method: http.MethodGet, resolve: []outboundhttp.ResolveMapping{{Host: "issuer.example", Address: netip.MustParseAddr("192.0.2.2")}}}, true},
		"mapped hostname":    {requestSpec{target: target, method: http.MethodGet, resolve: []outboundhttp.ResolveMapping{{Host: "issuer.example", TargetHost: "recipient.example", TargetPort: 8443}}}, true},
		"other host mapping": {requestSpec{target: target, method: http.MethodGet, resolve: []outboundhttp.ResolveMapping{{Host: "other.example", Address: netip.MustParseAddr("192.0.2.2")}}}, false},
		"other port mapping": {requestSpec{target: target, method: http.MethodGet, resolve: []outboundhttp.ResolveMapping{{Host: "issuer.example", Port: 8443, Address: netip.MustParseAddr("192.0.2.2")}}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			store := httpcookies.New(nil)
			store.SetCookies(target, []*http.Cookie{{Name: "session", Value: "private-cookie-4411", Path: "/"}})
			jar := &secretcap.CookieJar{Name: "jar", Reference: "secret://cookie-jar", Store: store}

			asks := 0
			deps := Deps{SecretMatcher: testSecretMatcher(t), SecretAsk: func(_ context.Context, a secretmatch.Alert) (secretmatch.Resolution, error) {
				asks++
				wantDest, wantLabel := requestDestination(tc.spec)
				if a.DestinationID != wantDest || a.DestinationLabel != wantLabel || a.Container != "cookie jar jar" || len(a.SecretNames) != 1 || a.SecretNames[0] != "session" {
					t.Errorf("alert = %+v, want dest (%q, %q)", a, wantDest, wantLabel)
				}
				return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
			}}
			beforeHop, err := prepareCookieDelivery(t.Context(), deps, tc.spec, jar)
			testutil.FailErr(t, "prepareCookieDelivery", err)
			testutil.FailErr(t, "beforeHop", beforeHop(t.Context(), outboundhttp.HopRequest{URL: target, Method: http.MethodGet, HostHeader: tc.spec.hostHeader}))
			delivered := jar.Store.Cookies(target)
			if tc.foreign && (asks != 1 || len(delivered) != 0) {
				t.Fatalf("foreign override sent cookie without withholding: asks=%d delivered=%+v", asks, delivered)
			}
			if !tc.foreign && (asks != 0 || len(delivered) != 1 || delivered[0].Value != "private-cookie-4411") {
				t.Fatalf("plain issuer failed to deliver cookie quietly: asks=%d delivered=%+v", asks, delivered)
			}

			// Conversely, a cookie captured on an overridden hop records the override
			// issuer and cannot be silently promoted into plain URL authority later.
			captureStore := httpcookies.New(nil)
			captureJar := &secretcap.CookieJar{Name: "jar", Reference: "secret://cookie-jar", Store: captureStore}
			captureHop, err := prepareCookieDelivery(t.Context(), deps, tc.spec, captureJar)
			testutil.FailErr(t, "prepare capture hop", err)
			testutil.FailErr(t, "run capture hop", captureHop(t.Context(), outboundhttp.HopRequest{URL: target, Method: http.MethodGet, HostHeader: tc.spec.hostHeader}))
			captureStore.SetCookies(target, []*http.Cookie{{Name: "captured", Value: "override-cookie-8822", Path: "/"}})

			plainAsks := 0
			plainDeps := Deps{SecretMatcher: testSecretMatcher(t), SecretAsk: func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
				plainAsks++
				return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
			}}
			plainHop, err := prepareCookieDelivery(t.Context(), plainDeps, plain, captureJar)
			testutil.FailErr(t, "prepare plain hop", err)
			testutil.FailErr(t, "run plain hop", plainHop(t.Context(), outboundhttp.HopRequest{URL: target, Method: http.MethodGet}))
			plainDelivered := captureStore.Cookies(target)
			if tc.foreign && (plainAsks != 1 || len(plainDelivered) != 0) {
				t.Fatalf("override-captured cookie promoted to plain authority without review: asks=%d delivered=%+v", plainAsks, plainDelivered)
			}
			if !tc.foreign && (plainAsks != 0 || len(plainDelivered) != 1) {
				t.Fatalf("plain-captured cookie required review on plain hop: asks=%d delivered=%+v", plainAsks, plainDelivered)
			}
		})
	}
}

func TestManagedCookieJarAndGrantFreshnessAcrossReviewWait(t *testing.T) {
	t.Run("jar_modified_during_egress_wait_cannot_send_unreviewed_cookie", func(t *testing.T) {
		secrets, _ := testSecrets(t)
		var receivedCookie string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedCookie = r.Header.Get("Cookie")
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)

		target, err := url.Parse(server.URL + "/api")
		testutil.FailErr(t, "parse target", err)
		seedJar, err := secrets.OpenCookieJar(t.Context(), cookieSeedRequest("seed-initial"))
		testutil.FailErr(t, "open seed jar", err)
		seedJar.Store.SetCookies(target, []*http.Cookie{{Name: "benign", Value: "public-cookie-value-1111", Path: "/"}})
		_, err = secrets.SaveCookieJar(t.Context(), cookieSeedRequest("seed-initial"), seedJar)
		testutil.FailErr(t, "save seed jar", err)

		confine.SetEgressPosture(confine.PostureAsk)
		release := confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
			// While egress route admission waits, another operation injects a foreign-issuer
			// cookie into the managed jar.
			mutated, openErr := secrets.OpenCookieJar(t.Context(), cookieSeedRequest("mutate-during-wait"))
			testutil.FailErr(t, "open jar during wait", openErr)
			mutated.Store.SetDelivery("retained-socket-issuer", nil)
			mutated.Store.SetCookies(target, []*http.Cookie{{Name: "injected", Value: "foreign-secret-cookie-9999", Path: "/"}})
			_, saveErr := secrets.SaveCookieJar(t.Context(), cookieSeedRequest("mutate-during-wait"), mutated)
			testutil.FailErr(t, "save jar during wait", saveErr)
			return true
		})
		t.Cleanup(func() {
			confine.SetEgressPosture(confine.PostureObserve)
			_ = release(context.Background())
		})

		var secretAsks atomic.Int64
		deps := Deps{
			Boundary:      testBoundary(),
			Secrets:       secrets,
			SecretMatcher: testSecretMatcher(t),
			SecretAsk: func(_ context.Context, a secretmatch.Alert) (secretmatch.Resolution, error) {
				secretAsks.Add(1)
				return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
			},
		}
		_, err = runRequest(t, deps, map[string]any{
			"url":                server.URL + "/api",
			"cookie_jar":         "service",
			"capability_request": loopbackCapability(t, server.URL),
		}, sessionContext(t.TempDir(), "egress-wait-call"))
		testutil.FailErr(t, "request after egress wait", err)
		if secretAsks.Load() != 1 {
			t.Fatalf("secretAsks = %d, want 1 (injected foreign cookie must be reviewed after egress wait)", secretAsks.Load())
		}
		if strings.Contains(receivedCookie, "foreign-secret-cookie-9999") {
			t.Fatalf("server received withheld foreign cookie injected during wait: %q", receivedCookie)
		}
		if receivedCookie != "benign=public-cookie-value-1111" {
			t.Fatalf("server received Cookie %q, want %q", receivedCookie, "benign=public-cookie-value-1111")
		}
	})

	t.Run("jar_revoked_during_secret_review_wait_stops_before_delivery", func(t *testing.T) {
		secrets, _ := testSecrets(t)
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)
		seedRetainedIssuerCookie(t, secrets, server.URL)
		opened, err := secrets.OpenCookieJar(t.Context(), cookieSeedRequest("lookup-ref"))
		testutil.FailErr(t, "open seeded jar", err)

		deps := Deps{
			Boundary:      testBoundary(),
			Secrets:       secrets,
			SecretMatcher: testSecretMatcher(t),
			SecretAsk: func(ctx context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
				// While the secret review card is waiting, the cookie jar is revoked.
				_, revokeErr := secrets.RevokeByAgent(ctx, testdbseed.DefaultProjectID, "root-1", opened.Reference)
				testutil.FailErr(t, "revoke jar during secret ask", revokeErr)
				return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
			},
		}
		_, err = runRequest(t, deps, map[string]any{
			"url":                server.URL + "/private",
			"cookie_jar":         "service",
			"capability_request": loopbackCapability(t, server.URL),
		}, sessionContext(t.TempDir(), "revoked-during-wait"))
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != CookieJarFailedCode {
			t.Fatalf("err = %v, want %s when jar is revoked during review wait", err, CookieJarFailedCode)
		}
		if requests.Load() != 0 {
			t.Fatalf("server received %d requests after jar revocation during wait, want 0", requests.Load())
		}
	})

	t.Run("unbacked_frontend_cookie_jar_fails_closed_before_delivery", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)

		deps := Deps{
			Boundary:      testBoundary(),
			SecretMatcher: testSecretMatcher(t),
		}
		_, err := Perform(t.Context(), deps, map[string]any{
			"url":                server.URL + "/api",
			"capability_request": loopbackCapability(t, server.URL),
		}, sessionContext(t.TempDir(), "unbacked-jar"), ExchangeOptions{
			Jar: unbackedCookieJar{},
		})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != toolrejection.OutboundSecretScreenFailedCode {
			t.Fatalf("Perform with unbacked Jar err = %v, want %s", err, toolrejection.OutboundSecretScreenFailedCode)
		}
		if requests.Load() != 0 {
			t.Fatalf("server received %d requests with unbacked Jar, want 0", requests.Load())
		}
	})
}

type unbackedCookieJar struct{}

func (unbackedCookieJar) SetCookies(*url.URL, []*http.Cookie) {}
func (unbackedCookieJar) Cookies(*url.URL) []*http.Cookie {
	return []*http.Cookie{{Name: "session", Value: "unreviewed"}}
}

