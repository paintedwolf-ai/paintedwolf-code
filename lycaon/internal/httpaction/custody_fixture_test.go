package httpaction

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// hostSecret stores value as a host-generated project secret. Its release
// follows posture and review rather than a person's presence, so a test of
// request mechanics exercises the wire, not custody.
func hostSecret(t *testing.T, service *secretcap.Service, operation, name, value string) secretcap.Metadata {
	t.Helper()
	put, err := service.Put(t.Context(), secretcap.PutRequest{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "fixture-session", OperationID: operation,
		Name: name, Purpose: "request mechanics", Scope: secretcap.ScopeProject, Origin: secretcap.OriginGenerated,
		Value: value, Format: secretcap.FormatBase64URL, EntropyBits: 256,
	})
	testutil.FailErr(t, "store "+name, err)
	return put.Metadata
}

// A value a person stored leaves over HTTP only under a reviewed release
// while its chat is unlocked; a locked chat's request never reaches the server.
func TestHeldValueLeavesOnlyWhileItsChatIsUnlocked(t *testing.T) {
	for name, unlocked := range map[string]bool{"locked": false, "unlocked": true} {
		t.Run(name, func(t *testing.T) {
			service, owner := managedRequestService(t)
			fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("h", 32)))
			testutil.FailErr(t, "build fingerprinter", err)
			service.SetFingerprinter(fingerprinter)
			meta, err := service.CreateSettingsSecret(t.Context(), secretcap.CreateSettingsSecretRequest{
				ProjectID: testdbseed.DefaultProjectID, PersonID: owner, OperationID: "held", Name: "Deploy token",
				Purpose: "deploy", Value: "person-held-deploy-token",
			})
			testutil.FailErr(t, "create held secret", err)
			var received atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				received.Add(1)
				_, _ = io.WriteString(w, "ok")
			}))
			defer server.Close()
			args := map[string]any{
				"url": server.URL, "auth": map[string]any{"scheme": "bearer", "token": meta.Reference},
				"capability_request": loopbackCapability(t, server.URL),
			}
			registry := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register HTTP", Register(registry, Deps{Boundary: testBoundary(), SecretMatcher: testSecretMatcher(t),
				SecretAsk: func(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
					secretcap.ResolutionFrom(ctx).ApproveRelease(secretcap.Release{Fingerprints: alert.Fingerprints})
					return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
				},
			}))
			unlocks := presence.NewUnlocks()
			if unlocked {
				unlocks.Open(presence.NewUnlock("chat-1", presence.Verified{
					ChallengeID: "11111111-1111-4111-8111-111111111111", PersonID: owner,
					Authenticator: presence.AuthenticatorMacOS, WindowLabel: "main", VerifiedAt: time.Now(),
				}))
			}
			resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolName: "http_request", ToolCallID: "held"})
			testutil.FailErr(t, "resolve request", err)
			resolved.InChatForTest("chat-1", unlocks)
			ctx := secretcap.WithResolution(t.Context(), resolved)
			_, runErr := registry.Run(ctx, "http_request", resolved.Arguments, tools.ToolContext{CanonicalArgs: args, Secrets: resolved})
			resolved.Finish(t.Context())
			if !unlocked {
				reject := toolrejection.AsToolReject(runErr)
				if reject == nil || reject.Code != toolrejection.OutboundSecretScreenFailedCode || reject.Data["fault_stage"] != secretmatch.FaultStageVaultLocked {
					t.Fatalf("locked release result = %v", runErr)
				}
				if received.Load() != 0 {
					t.Fatal("a locked chat's request reached the server")
				}
				return
			}
			testutil.FailErr(t, "unlocked release", runErr)
			if received.Load() != 1 {
				t.Fatalf("server received %d requests", received.Load())
			}
		})
	}
}
