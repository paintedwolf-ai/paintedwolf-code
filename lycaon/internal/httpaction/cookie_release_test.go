package httpaction

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/httpcookies"
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
