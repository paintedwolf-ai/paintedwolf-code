package httpaction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
		"url": server.URL + "/start", "method": "POST", "body": "payload", "redirects": "safe",
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

// A same-origin redirect resends the jar's token and the screened headers. A
// jar revoked, or a classification withdrawn, while the first hop ran stops
// the replay before it leaves, as either does before the first hop.
func TestRedirectReplayRechecksTheJarAndClassification(t *testing.T) {
	for _, mode := range []string{"token_jar_revoked", "classification_withdrawn"} {
		t.Run(mode, func(t *testing.T) {
			service, _, meta, matcher, _, opened, now := newHeldSetup(t)
			*now = opened.Add(time.Minute)
			issuer := issuerServer(t)
			root := t.TempDir()
			captureIssuedToken(t, Deps{Boundary: testBoundary(), Secrets: service, SecretMatcher: matcher}, issuer, root)
			openedJar, err := service.OpenTokenJar(t.Context(), secretcap.TokenJarRequest{
				ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "lookup", OperationID: "lookup-op", Name: "jar",
			})
			testutil.FailErr(t, "open seeded token jar", err)
			var ignoreActive atomic.Bool
			ignoreActive.Store(true)
			fp, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("d", 32)))
			testutil.FailErr(t, "fp", err)
			ignoredFP := fp.Fingerprint(plantedBraveKey)
			matcher.SetIgnoredSource(func(context.Context) map[secretmatch.SecretFingerprint]bool {
				if ignoreActive.Load() {
					return map[secretmatch.SecretFingerprint]bool{ignoredFP: true}
				}
				return nil
			})

			var replays atomic.Int32
			relying := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/next" {
					replays.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				switch mode {
				case "token_jar_revoked":
					_, revErr := service.RevokeByAgent(context.Background(), testdbseed.DefaultProjectID, "root-1", openedJar.Reference)
					testutil.FailErr(t, "revoke token jar during the first hop", revErr)
				case "classification_withdrawn":
					ignoreActive.Store(false)
				}
				http.Redirect(w, r, "/next", http.StatusTemporaryRedirect)
			}))
			defer relying.Close()

			t.Cleanup(func() { confine.SetEgressResolver(nil) })
			confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
				return true
			})
			args := map[string]any{
				"url": relying.URL + "/api", "redirects": "safe", "token_jar": "jar",
				"auth":               map[string]any{"scheme": "bearer", "token": meta.Reference},
				"headers":            []any{map[string]any{"name": "X-Service-Token", "value": "{{token:api}}"}, map[string]any{"name": "X-Subscription-Token", "value": plantedBraveKey}},
				"capability_request": loopbackCapability(t, relying.URL),
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
				ToolName: "http_request", ToolCallID: "redirect-" + mode,
			})
			testutil.FailErr(t, "resolve", err)
			tc := sessionContext(root, "redirect-"+mode)
			tc.Effects.CanonicalArgs = args
			tc.Effects.Secrets = resolved
			ctx := secretcap.WithResolution(t.Context(), resolved)
			_, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{})
			resolved.Finish(t.Context())

			reject := toolrejection.AsToolReject(runErr)
			want := TokenJarFailedCode
			if mode == "classification_withdrawn" {
				want = toolrejection.OutboundSecretScreenFailedCode
			}
			if reject == nil || reject.Code != want {
				t.Fatalf("replay after %s = %v (reject=%+v), want %s", mode, runErr, reject, want)
			}
			if replays.Load() != 0 {
				t.Fatalf("the replay left after %s: %d", mode, replays.Load())
			}
		})
	}
}
