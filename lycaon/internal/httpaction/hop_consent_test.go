package httpaction

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Approving a cookie's disclosure to a recipient is consent to the connection
// that carries it, under the same recipient identity, overrides included;
// withholding the cookie consents to nothing.
func TestCookieApprovalConsentsToItsConnection(t *testing.T) {
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve); restoreResolver() })
	target, err := url.Parse("https://issuer.example/api")
	testutil.FailErr(t, "parse target", err)
	spec := requestSpec{target: target, method: http.MethodGet, hostHeader: "other.example"}
	hop := outboundhttp.HopRequest{URL: target, Method: http.MethodGet, HostHeader: spec.hostHeader}

	for i, decision := range []secretmatch.Decision{secretmatch.SendUnchanged, secretmatch.Withhold} {
		store := httpcookies.New(nil)
		store.SetCookies(target, []*http.Cookie{{Name: "session", Value: "private-cookie-4411", Path: "/"}})
		jar := &secretcap.CookieJar{Name: "jar", Reference: "secret://cookie-jar", Store: store}
		deps := Deps{SecretMatcher: testSecretMatcher(t), SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			return secretmatch.Resolution{Decision: decision}, nil
		}}
		ctx := hitl.WithStopContext(egressgate.WithReviewedRecipients(t.Context()), t.Context())
		beforeHop, err := prepareCookieDelivery(ctx, deps, spec, jar)
		testutil.FailErr(t, "prepare cookies", err)
		testutil.FailErr(t, "cookie review", beforeHop(ctx, hop))

		var consented []bool
		confine.SetEgressResolver(func(ctx context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
			consented = append(consented, egressgate.RequestConsented(ctx, ep.Host, ep.Port))
			return true
		})
		command := confine.EgressCommand{SessionID: "cookie-consent", ToolCallID: []string{"sent", "withheld"}[i]}
		recipientID, _ := requestDestination(spec.forHop(hop))
		testutil.FailErr(t, "admit hop", admitHopRoute(egressgate.WithAttribution(ctx, command), target, http.MethodGet, nil, recipientID))
		confine.ForgetEgressAction(command.SessionID, command.ToolCallID)
		want := decision == secretmatch.SendUnchanged
		if len(consented) != 1 || consented[0] != want {
			t.Fatalf("%v: connection consent %v, want %v", decision, consented, want)
		}
	}
}
