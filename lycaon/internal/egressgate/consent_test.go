package egressgate

import (
	"context"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHTTPConsentIsBoundToOriginOnEveryHop(t *testing.T) {
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve); confine.SetEgressResolver(nil) })
	target, err := url.Parse("https://service.test:443/login")
	testutil.FailErr(t, "parse service", err)
	id, label := secretmatch.HTTPDestination(target)
	resolution := &secretcap.Resolution{}
	resolution.ApproveRelease(secretcap.Release{Fingerprints: []secretmatch.SecretFingerprint{"value"}, Recipients: []secretmatch.Recipient{{ID: id, Label: label, Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}}})
	ctx := secretcap.WithResolution(t.Context(), resolution)
	ctx = hitl.WithStopContext(ctx, t.Context())
	for i, hop := range []struct {
		address   string
		consented bool
	}{
		{"https://service.test/profile", true},
		{"http://service.test:443/profile", false},
		{"https://service.test:444/profile", false},
		{"https://other.test/profile", false},
		{"https://SERVICE.test/profile", true},
	} {
		called := false
		confine.SetEgressResolver(func(ctx context.Context, _ confine.EgressCommand, endpoint egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
			called = true
			if got := RequestConsented(ctx, endpoint.Host, endpoint.Port); got != hop.consented {
				t.Errorf("hop %s consent=%v want %v", hop.address, got, hop.consented)
			}
			return true
		})
		command := confine.EgressCommand{SessionID: "consent-test", ToolCallID: []string{"first", "scheme", "port", "host", "case"}[i]}
		attributed := WithAttribution(ctx, command)
		next, err := url.Parse(hop.address)
		testutil.FailErr(t, "parse next hop", err)
		testutil.FailErr(t, "review next hop", AwaitHTTPRequest(attributed, next, "GET"))
		confine.ForgetEgressAction(command.SessionID, command.ToolCallID)
		if !called {
			t.Fatal("endpoint decision was bypassed")
		}
	}
}
