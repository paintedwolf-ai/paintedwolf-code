package httpaction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// A same-origin 307 replays the request's headers and body. A release revoked
// while the first hop ran refuses the replay; the second hop never leaves.
func TestRedirectReplayRevalidatesTheRelease(t *testing.T) {
	service, owner, meta, matcher, _, opened, now := newHeldSetup(t)
	*now = opened.Add(time.Minute)
	var replays atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			replays.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, err := service.RevokeProject(context.Background(), testdbseed.DefaultProjectID, meta.Reference, owner)
		testutil.FailErr(t, "revoke during the first hop", err)
		http.Redirect(w, r, "/next", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	args := map[string]any{
		"url": server.URL + "/start", "method": "POST", "body": "payload",
		"auth":               map[string]any{"scheme": "bearer", "token": meta.Reference},
		"capability_request": loopbackCapability(t, server.URL),
	}
	deps := Deps{
		Boundary: testBoundary(), Secrets: service, SecretMatcher: matcher,
		SecretAsk: func(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
			secretcap.ResolutionFrom(ctx).ApproveRelease(secretcap.Release{
				Fingerprints: alert.Fingerprints,
				Recipients:   []secretmatch.Recipient{{ID: alert.DestinationID, Label: alert.DestinationLabel, Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}},
			})
			return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
		},
	}
	resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		ToolName: "http_request", ToolCallID: "redirect-revoked",
	})
	testutil.FailErr(t, "resolve", err)
	tc := sessionContext(t.TempDir(), "redirect-revoked")
	tc.Effects.CanonicalArgs = args
	tc.Effects.Secrets = resolved
	_, runErr := Perform(secretcap.WithResolution(t.Context(), resolved), deps, resolved.Arguments, tc, ExchangeOptions{})
	resolved.Finish(t.Context())

	if reject := toolrejection.AsToolReject(runErr); reject == nil || reject.Code != "SECRET_REFERENCE_REVOKED" {
		t.Fatalf("replay after revocation = %v (reject=%+v), want SECRET_REFERENCE_REVOKED", runErr, reject)
	}
	if replays.Load() != 0 {
		t.Fatalf("the revoked release was replayed %d times", replays.Load())
	}
}
