package httpaction

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// managedRequestService returns a vault and the owner its store seeds.
func managedRequestService(t *testing.T) (*secretcap.Service, string) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets, Context: "request test",
	}, func(string) bool { return true })
	return secretcap.NewWithStore(database, values, nil), testdbseed.OwnerID(t, database)
}

func TestRedactionReusesReviewedUploadBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "upload.txt")
	testutil.FailErr(t, "write reviewed upload", os.WriteFile(path, []byte("reviewed bytes"), 0o600))
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reader, err := req.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Error(err)
				return
			}
			if part.FileName() != "" {
				received = string(body)
			}
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register upload", Register(registry, Deps{Boundary: testBoundary(), SecretMatcher: testSecretMatcher(t),
		SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			testutil.FailErr(t, "change upload during review", os.WriteFile(path, []byte("unreviewed replacement"), 0o600))
			return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
		},
	}))
	_, err := registry.Run(t.Context(), "http_request", map[string]any{
		"method": "POST", "url": server.URL,
		"headers":            []any{map[string]any{"name": "X-Subscription-Token", "value": plantedBraveKey}},
		"form":               []any{map[string]any{"name": "upload", "path": "upload.txt"}},
		"capability_request": loopbackCapability(t, server.URL),
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "root"},
	})
	testutil.FailErr(t, "send reviewed upload", err)
	if received != "reviewed bytes" {
		t.Fatal("redaction reread an upload after review")
	}
}

func TestHTTPDoesNotTreatUnusedAuthFieldsAsDisclosure(t *testing.T) {
	service, _ := managedRequestService(t)
	meta := hostSecret(t, service, "unused", "Unused", "unused-password")
	args := map[string]any{"auth": map[string]any{"scheme": "bearer", "token": "public-test-value", "password": meta.Reference}, "response_path": meta.Reference}
	resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "resolve unused fields", err)
	evidence, err := requestArgumentEvidence(t.Context(), testSecretMatcher(t), resolved.Arguments, resolved)
	testutil.FailErr(t, "screen transmitted fields", err)
	if len(evidence) != 0 {
		t.Fatal("local controls or unused fields raised a disclosure")
	}
	resolved.HandOff(t.Context(), func(path string) bool { return secretmatch.HTTPArgumentConsumed(resolved.Arguments, path) })
	resolved.Finish(t.Context())
	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
	testutil.FailErr(t, "read unused secret history", err)
	if len(history.Items) != 1 || history.Items[0].Delivery != secretcap.DeliveryNotDispatched {
		t.Fatal("unused secret was reported handed off")
	}
}

func TestManagedWireProvenancePreservesUnrelatedSecretDetection(t *testing.T) {
	service, _ := managedRequestService(t)
	meta := hostSecret(t, service, "mixed", "Managed", "managed-opaque-value")
	args := map[string]any{"body_text": "X-Subscription-Token: " + plantedBraveKey + "\nManaged: " + meta.Reference}
	resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "resolve mixed field", err)
	matcher := testSecretMatcher(t)
	evidence, err := requestArgumentEvidence(t.Context(), matcher, resolved.Arguments, resolved)
	testutil.FailErr(t, "screen mixed field", err)
	if len(secretmatch.Fingerprints(evidence)) != 2 {
		t.Fatal("managed provenance hid another credential in the same field")
	}
	redacted := redactRequestArguments(t.Context(), matcher, resolved.Arguments, tools.ToolContext{
		Effects: tools.InvocationEffects{Secrets: resolved},
	})
	body := redacted["body_text"].(string)
	if strings.Contains(body, plantedBraveKey) || strings.Contains(body, "managed-opaque-value") {
		t.Fatal("mixed field was not fully redacted")
	}
}

func TestManagedSecretPolicySurvivesHTTPEncoding(t *testing.T) {
	for _, decision := range []secretmatch.Decision{secretmatch.SendUnchanged, secretmatch.SendRedacted, secretmatch.Withhold} {
		t.Run(string(decision), func(t *testing.T) {
			service, _ := managedRequestService(t)
			const value = " 4321&\"<>\\unicode-λ "
			meta := hostSecret(t, service, "create", "Password", value)
			var received atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				received.Add(1)
				_, password, ok := req.BasicAuth()
				if !ok {
					t.Error("Basic authentication is malformed")
				}
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				for _, got := range []string{password, req.URL.Query().Get("credential"), body["credential"].(string)} {
					if decision == secretmatch.SendUnchanged && got != value {
						t.Error("serialization changed password bytes")
					}
					if decision == secretmatch.SendRedacted && strings.Contains(got, value) {
						t.Error("encoded credential survived redaction")
					}
				}
				_, _ = io.WriteString(w, "ok")
			}))
			defer server.Close()
			args := map[string]any{
				"method": "POST", "url": server.URL,
				"auth":               map[string]any{"scheme": "basic", "username": "user", "password": meta.Reference},
				"query":              []any{map[string]any{"name": "credential", "value": meta.Reference}},
				"body_json":          map[string]any{"credential": meta.Reference},
				"capability_request": loopbackCapability(t, server.URL),
			}
			matcher := testSecretMatcher(t)
			expected, err := matcher.ManagedValue(value, "Password", meta.Reference)
			testutil.FailErr(t, "derive expected identity", err)
			asks := 0
			registry := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register HTTP", Register(registry, Deps{Boundary: testBoundary(), SecretMatcher: matcher,
				SecretAsk: func(_ context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
					asks++
					if len(alert.Fingerprints) != 1 || alert.Fingerprints[0] != expected.Fingerprint {
						t.Error("encoding changed secret release identity")
					}
					return secretmatch.Resolution{Decision: decision, ReceiptToken: "receipt"}, nil
				},
			}))
			// A send decision leaves the managed reference reusable.
			for range 2 {
				resolved, resolveErr := service.Resolve(t.Context(), args, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolName: "http_request", ToolCallID: "request"})
				testutil.FailErr(t, "resolve request", resolveErr)
				_, runErr := registry.Run(t.Context(), "http_request", resolved.Arguments, tools.ToolContext{
					Effects: tools.InvocationEffects{CanonicalArgs: args,
						Secrets: resolved},
				})
				resolved.Finish(t.Context())
				if decision == secretmatch.Withhold {
					if reject := toolrejection.AsToolReject(runErr); reject == nil || reject.Code != toolrejection.OutboundSecretDeniedCode {
						t.Fatalf("unexpected withhold result: %v", runErr)
					}
				} else {
					testutil.FailErr(t, "send request", runErr)
				}
			}
			if asks != 2 {
				t.Fatalf("got %d decision calls for two invocations", asks)
			}
			wantReceived := int32(2)
			if decision == secretmatch.Withhold {
				wantReceived = 0
			}
			if received.Load() != wantReceived {
				t.Fatalf("received %d requests, want %d", received.Load(), wantReceived)
			}
			history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
			testutil.FailErr(t, "read delivery history", err)
			want := secretcap.DeliveryHandedOff
			if decision == secretmatch.Withhold {
				want = secretcap.DeliveryWithheld
			}
			if decision == secretmatch.SendRedacted {
				want = secretcap.DeliveryRedacted
			}
			for _, use := range history.Items {
				if use.Delivery != want || use.ToolCallID != "request" {
					t.Fatalf("unexpected delivery history: %+v", use)
				}
			}
		})
	}
}

func TestHTTPSecretDeliveryTimingAndLiveRevalidation(t *testing.T) {
	const (
		heldSecretVal = "person-held-deploy-token-9988"
		cookieSecret  = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
		unlockID      = "11111111-1111-4111-8111-111111111111"
	)

	newHeldSetup := func(t *testing.T) (*secretcap.Service, string, secretcap.Metadata, *secretmatch.Matcher, *presence.Unlocks, time.Time, *time.Time) {
		t.Helper()
		service, owner := managedRequestService(t)
		fp, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("d", 32)))
		testutil.FailErr(t, "fingerprinter", err)
		service.SetFingerprinter(fp)
		matcher := testSecretMatcher(t)
		matcher.SetFingerprinter(fp)
		unlocks := presence.NewUnlocks(presence.NewBroker())
		service.SetUnlocks(unlocks)
		opened := time.Now().UTC().Truncate(time.Second)
		now := opened
		unlocks.SetClock(func() time.Time { return now })
		unlocks.Open(presence.NewUnlock("root-1", presence.Verified{
			ChallengeID: unlockID, PersonID: owner, Authenticator: presence.AuthenticatorMacOS,
			WindowLabel: "main", VerifiedAt: opened,
		}))
		meta, err := service.CreateSettingsSecret(t.Context(), secretcap.CreateSettingsSecretRequest{
			ProjectID: testdbseed.DefaultProjectID, PersonID: owner, OperationID: "held-secret",
			Name: "Deploy token", Purpose: "deploy", Value: heldSecretVal,
		})
		testutil.FailErr(t, "create held secret", err)
		return service, owner, meta, matcher, unlocks, opened, &now
	}

	t.Run("locked_during_hop0_wait_zero_disclosure", func(t *testing.T) {
		service, _, meta, matcher, unlocks, _, now := newHeldSetup(t)
		*now = (*now).Add(time.Minute)
		var hits atomic.Int32
		var seenAuth atomic.Value
		seenAuth.Store("")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			seenAuth.Store(r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, "ok")
		}))
		defer server.Close()

		t.Cleanup(func() {
			confine.SetEgressPostureResolver(nil)
			confine.SetApprovalsDisabledSource(nil)
			confine.SetEgressResolver(nil)
		})
		confine.SetApprovalsDisabledSource(func(confine.EgressCommand) bool { return false })
		confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture { return confine.PostureAsk })
		confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
			unlocks.Lock("root-1", presence.EndManual)
			return true
		})

		args := map[string]any{
			"url":                server.URL + "/deploy",
			"auth":               map[string]any{"scheme": "bearer", "token": meta.Reference},
			"capability_request": loopbackCapability(t, server.URL),
		}
		deps := Deps{
			Boundary:      testBoundary(),
			Secrets:       service,
			SecretMatcher: matcher,
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
			ToolName: "http_request", ToolCallID: "locked-wait",
		})
		testutil.FailErr(t, "resolve", err)
		tc := sessionContext(t.TempDir(), "locked-wait")
		tc.Effects.CanonicalArgs = args
		tc.Effects.Secrets = resolved
		ctx := secretcap.WithResolution(t.Context(), resolved)
		_, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{})
		resolved.Finish(t.Context())

		reject := toolrejection.AsToolReject(runErr)
		if reject == nil || reject.Code != toolrejection.OutboundSecretScreenFailedCode || reject.Data["fault_stage"] != secretmatch.FaultStageVaultLocked {
			t.Fatalf("locked during wait err = %v (reject=%+v), want %s with fault_stage=%s", runErr, reject, toolrejection.OutboundSecretScreenFailedCode, secretmatch.FaultStageVaultLocked)
		}
		if hits.Load() != 0 || seenAuth.Load().(string) != "" {
			t.Fatalf("server hits=%d auth=%q after vault lock during wait, want 0 and empty", hits.Load(), seenAuth.Load())
		}
		history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
		testutil.FailErr(t, "read use history", err)
		if len(history.Items) != 1 || history.Items[0].Delivery != secretcap.DeliveryWithheld || history.Items[0].UnlockID != "" {
			t.Fatalf("use history = %+v, want DeliveryWithheld with empty UnlockID", history.Items)
		}
	})

	t.Run("pre_send_refusal_cancellation_and_pre_wire_failure_preserve_unlock_and_not_dispatched", func(t *testing.T) {
		for _, mode := range []string{"route_denied", "request_canceled", "pre_wire_dial_failure"} {
			t.Run(mode, func(t *testing.T) {
				service, _, meta, matcher, unlocks, opened, now := newHeldSetup(t)
				*now = opened.Add(time.Minute)
				var hits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					_, _ = io.WriteString(w, "ok")
				}))
				targetURL := server.URL + "/api"
				loopbackCap := loopbackCapability(t, server.URL)
				if mode == "pre_wire_dial_failure" {
					server.Close()
				} else {
					defer server.Close()
				}

				callCtx, cancel := context.WithCancel(t.Context())
				defer cancel()
				t.Cleanup(func() {
					confine.SetEgressPostureResolver(nil)
					confine.SetApprovalsDisabledSource(nil)
					confine.SetEgressResolver(nil)
				})
				confine.SetApprovalsDisabledSource(func(confine.EgressCommand) bool { return false })
				confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture { return confine.PostureAsk })
				confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
					switch mode {
					case "route_denied":
						return false
					case "request_canceled":
						cancel()
						return true
					default:
						return true
					}
				})

				args := map[string]any{
					"url":                targetURL,
					"auth":               map[string]any{"scheme": "bearer", "token": meta.Reference},
					"capability_request": loopbackCap,
				}
				deps := Deps{
					Boundary:      testBoundary(),
					Secrets:       service,
					SecretMatcher: matcher,
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
					ToolName: "http_request", ToolCallID: mode,
				})
				testutil.FailErr(t, "resolve", err)
				tc := sessionContext(t.TempDir(), mode)
				tc.Effects.CanonicalArgs = args
				tc.Effects.Secrets = resolved
				ctx := secretcap.WithResolution(callCtx, resolved)
				_, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{})
				resolved.Finish(t.Context())

				switch mode {
				case "route_denied":
					reject := toolrejection.AsToolReject(runErr)
					if reject == nil || reject.Code != "HTTP_REQUEST_HOST_DENIED" {
						t.Fatalf("route_denied err = %v (reject=%+v), want HTTP_REQUEST_HOST_DENIED", runErr, reject)
					}
				case "request_canceled":
					if !errors.Is(runErr, context.Canceled) {
						t.Fatalf("request_canceled err = %v, want context.Canceled", runErr)
					}
				case "pre_wire_dial_failure":
					reject := toolrejection.AsToolReject(runErr)
					if reject == nil || reject.Code != "HTTP_REQUEST_FAILED" || reject.Data["http_ResponsesReceived"] != nil && reject.Data["http_responses_received"] != 0 {
						t.Fatalf("pre_wire_dial_failure err = %v (reject=%+v), want HTTP_REQUEST_FAILED with 0 responses", runErr, reject)
					}
				}
				if hits.Load() != 0 {
					t.Fatalf("%s server hits = %d, want 0", mode, hits.Load())
				}
				history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
				testutil.FailErr(t, "read use history", err)
				if len(history.Items) != 1 || history.Items[0].Delivery != secretcap.DeliveryNotDispatched || history.Items[0].UnlockID != "" {
					t.Fatalf("%s use history = %+v, want DeliveryNotDispatched with empty UnlockID", mode, history.Items)
				}
				unlock, active := unlocks.Active("root-1")
				if !active || !unlock.LastUsedAt.Equal(opened) {
					t.Fatalf("%s unlock active=%v LastUsedAt=%v, want active with LastUsedAt=%v", mode, active, unlock.LastUsedAt, opened)
				}
			})
		}
	})

	t.Run("partial_unknown_send_vs_completed_earlier_hop_and_authorized_success", func(t *testing.T) {
		t.Run("early_401_413_or_expect_100_continue_refusal_before_body_sent_does_not_commit_body_secret", func(t *testing.T) {
			service, _, bodyMeta, matcher, unlocks, opened, now := newHeldSetup(t)
			*now = opened.Add(time.Minute)
			headerMeta := hostSecret(t, service, "header-op", "HeaderSecret", "header-secret-token-7766")

			var serverWire atomic.Value
			serverWire.Store("")
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			testutil.FailErr(t, "listen tcp", err)
			defer ln.Close()
			go func() {
				conn, acceptErr := ln.Accept()
				if acceptErr != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				var captured []byte
				buf := make([]byte, 512)
				for !strings.Contains(string(captured), "\r\n\r\n") {
					n, readErr := conn.Read(buf)
					if n > 0 {
						captured = append(captured, buf[:n]...)
					}
					if readErr != nil {
						break
					}
				}
				// Immediately refuse with 401 before sending 100 Continue, then observe whether any body bytes ever arrive.
				_, _ = io.WriteString(conn, "HTTP/1.1 401 Unauthorized\r\nContent-Length: 12\r\nConnection: close\r\n\r\nunauthorized")
				_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				rest, _ := io.ReadAll(conn)
				captured = append(captured, rest...)
				serverWire.Store(string(captured))
			}()

			rawURL := "http://" + ln.Addr().String() + "/upload"
			args := map[string]any{
				"method": "POST",
				"url":    rawURL,
				"headers": []any{
					map[string]any{"name": "Expect", "value": "100-continue"},
					map[string]any{"name": "X-Header-Secret", "value": headerMeta.Reference},
				},
				"body_text":          "payload=" + bodyMeta.Reference,
				"capability_request": loopbackCapability(t, rawURL),
			}
			deps := Deps{
				Boundary:      testBoundary(),
				Secrets:       service,
				SecretMatcher: matcher,
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
				ToolName: "http_request", ToolCallID: "early-401-before-body",
			})
			testutil.FailErr(t, "resolve", err)
			tc := sessionContext(t.TempDir(), "early-401-before-body")
			tc.Effects.CanonicalArgs = args
			tc.Effects.Secrets = resolved
			ctx := secretcap.WithResolution(t.Context(), resolved)
			ex, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{})
			resolved.Finish(t.Context())
			testutil.FailErr(t, "perform early 401", runErr)
			if ex.Response.Status != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", ex.Response.Status)
			}

			// Independent server byte oracle: header secret reached the server, body secret did not.
			var wireReceived string
			testutil.WaitFor(t, func() bool {
				wireReceived = serverWire.Load().(string)
				return wireReceived != ""
			})
			if !strings.Contains(wireReceived, "header-secret-token-7766") {
				t.Fatalf("server wire missing sent header secret: %q", wireReceived)
			}
			if strings.Contains(wireReceived, heldSecretVal) {
				t.Fatalf("server wire received body secret despite early 401 refusal before 100-continue: %q", wireReceived)
			}

			// Header secret was written to the wire -> DeliveryHandedOff.
			headerHistory, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, headerMeta.Reference, 0)
			testutil.FailErr(t, "read header secret history", err)
			if len(headerHistory.Items) != 1 || headerHistory.Items[0].Delivery != secretcap.DeliveryHandedOff {
				t.Fatalf("header secret history = %+v, want DeliveryHandedOff", headerHistory.Items)
			}

			// Body person-held secret was never written to the wire -> DeliveryNotDispatched and vault unlock untouched!
			bodyHistory, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, bodyMeta.Reference, 0)
			testutil.FailErr(t, "read body secret history", err)
			if len(bodyHistory.Items) != 1 || bodyHistory.Items[0].Delivery != secretcap.DeliveryNotDispatched || bodyHistory.Items[0].UnlockID != "" {
				t.Fatalf("body secret history = %+v, want DeliveryNotDispatched with empty UnlockID", bodyHistory.Items)
			}
			unlock, active := unlocks.Active("root-1")
			if !active || !unlock.LastUsedAt.Equal(opened) {
				t.Fatalf("early 401 body-unsent unlock active=%v LastUsedAt=%v, want untouched LastUsedAt=%v", active, unlock.LastUsedAt, opened)
			}
		})

		t.Run("server_receives_secret_bytes_then_closes_without_response_commits_handoff_and_reports_unknown_effect", func(t *testing.T) {
			service, _, meta, matcher, unlocks, opened, now := newHeldSetup(t)
			*now = opened.Add(time.Minute)
			var connHits atomic.Int32
			var serverWire atomic.Value
			serverWire.Store("")
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			testutil.FailErr(t, "listen tcp", err)
			defer ln.Close()
			go func() {
				conn, acceptErr := ln.Accept()
				if acceptErr != nil {
					return
				}
				connHits.Add(1)
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				var captured []byte
				buf := make([]byte, 512)
				for !strings.Contains(string(captured), heldSecretVal) {
					n, readErr := conn.Read(buf)
					if n > 0 {
						captured = append(captured, buf[:n]...)
					}
					if readErr != nil {
						break
					}
				}
				serverWire.Store(string(captured))
				_ = conn.Close()
			}()

			rawURL := "http://" + ln.Addr().String() + "/partial"
			args := map[string]any{
				"method":             "POST",
				"url":                rawURL,
				"body_text":          "payload=" + meta.Reference,
				"capability_request": loopbackCapability(t, rawURL),
			}
			deps := Deps{
				Boundary:      testBoundary(),
				Secrets:       service,
				SecretMatcher: matcher,
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
				ToolName: "http_request", ToolCallID: "partial-unknown",
			})
			testutil.FailErr(t, "resolve", err)
			tc := sessionContext(t.TempDir(), "partial-unknown")
			tc.Effects.CanonicalArgs = args
			tc.Effects.Secrets = resolved
			ctx := secretcap.WithResolution(t.Context(), resolved)
			_, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{})
			resolved.Finish(t.Context())

			var tf *TransportFailure
			if !errors.As(runErr, &tf) || tf.Reject == nil || tf.Reject.Code != "HTTP_REQUEST_FAILED" {
				t.Fatalf("partial send err = %v, want TransportFailure HTTP_REQUEST_FAILED", runErr)
			}
			// HTTP exchange outcome remains unknown (started=1, received=0), never recipient-completed.
			if tf.Reject.Data["http_requests_started"] != 1 || tf.Reject.Data["http_responses_received"] != 0 {
				t.Fatalf("partial send transport data = %+v, want started=1 received=0", tf.Reject.Data)
			}
			if connHits.Load() != 1 || !strings.Contains(serverWire.Load().(string), heldSecretVal) {
				t.Fatalf("connHits=%d serverWire=%q, want 1 and secret bytes observed on wire", connHits.Load(), serverWire.Load())
			}
			// Because the secret bytes were written to the socket and received by the server before close,
			// secretcap truthfully records DeliveryHandedOff and consumes the unlock.
			history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
			testutil.FailErr(t, "read use history", err)
			if len(history.Items) != 1 || history.Items[0].Delivery != secretcap.DeliveryHandedOff || history.Items[0].UnlockID != unlockID {
				t.Fatalf("partial send use history = %+v, want DeliveryHandedOff with unlockID=%s", history.Items, unlockID)
			}
			unlock, active := unlocks.Active("root-1")
			if !active || !unlock.LastUsedAt.Equal(*now) {
				t.Fatalf("partial send unlock active=%v LastUsedAt=%v, want LastUsedAt=%v", active, unlock.LastUsedAt, *now)
			}
		})

		t.Run("earlier_completed_hop_commits_once_when_hop1_denied", func(t *testing.T) {
			service, _, meta, matcher, unlocks, opened, now := newHeldSetup(t)
			*now = opened.Add(time.Minute)
			var originHits, targetHits atomic.Int32
			var originAuth atomic.Value
			originAuth.Store("")

			targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				targetHits.Add(1)
				_, _ = io.WriteString(w, "target-ok")
			}))
			defer targetServer.Close()

			originServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originHits.Add(1)
				originAuth.Store(r.Header.Get("Authorization"))
				http.Redirect(w, r, targetServer.URL+"/next", http.StatusFound)
			}))
			defer originServer.Close()

			targetPort := uint16(targetServer.Listener.Addr().(*net.TCPAddr).Port)
			t.Cleanup(func() {
				confine.SetEgressPostureResolver(nil)
				confine.SetApprovalsDisabledSource(nil)
				confine.SetEgressResolver(nil)
			})
			confine.SetApprovalsDisabledSource(func(confine.EgressCommand) bool { return false })
			confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture { return confine.PostureAsk })
			confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
				return ep.Port != targetPort
			})

			originCap := loopbackCapability(t, originServer.URL)
			targetCap := loopbackCapability(t, targetServer.URL)
			bothPorts := append([]any(nil), originCap["loopback_connect"].(map[string]any)["ports"].([]any)...)
			bothPorts = append(bothPorts, targetCap["loopback_connect"].(map[string]any)["ports"].([]any)...)
			bothCap := map[string]any{"loopback_connect": map[string]any{"ports": bothPorts}}

			args := map[string]any{
				"url":                originServer.URL + "/start",
				"redirects":          "safe",
				"auth":               map[string]any{"scheme": "bearer", "token": meta.Reference},
				"capability_request": bothCap,
			}
			deps := Deps{
				Boundary:      testBoundary(),
				Secrets:       service,
				SecretMatcher: matcher,
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
				ToolName: "http_request", ToolCallID: "hop0-completed-hop1-denied",
			})
			testutil.FailErr(t, "resolve", err)
			tc := sessionContext(t.TempDir(), "hop0-completed-hop1-denied")
			tc.Effects.CanonicalArgs = args
			tc.Effects.Secrets = resolved
			ctx := secretcap.WithResolution(t.Context(), resolved)
			_, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{})
			resolved.Finish(t.Context())

			var tf *TransportFailure
			if !errors.As(runErr, &tf) || tf.Reject == nil || tf.Reject.Code != "HTTP_REQUEST_HOST_DENIED" {
				t.Fatalf("hop1 denied err = %v, want TransportFailure HTTP_REQUEST_HOST_DENIED", runErr)
			}
			if tf.Reject.Data["http_requests_started"] != 1 || tf.Reject.Data["http_responses_received"] != 1 || tf.Reject.Data["http_last_status"] != http.StatusFound {
				t.Fatalf("hop1 denied transport data = %+v, want started=1 received=1 status=302", tf.Reject.Data)
			}
			if originHits.Load() != 1 || targetHits.Load() != 0 || originAuth.Load().(string) != "Bearer "+heldSecretVal {
				t.Fatalf("originHits=%d targetHits=%d originAuth=%q", originHits.Load(), targetHits.Load(), originAuth.Load())
			}
			history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
			testutil.FailErr(t, "read use history", err)
			if len(history.Items) != 1 || history.Items[0].Delivery != secretcap.DeliveryHandedOff || history.Items[0].UnlockID != unlockID {
				t.Fatalf("earlier completed hop use history = %+v, want DeliveryHandedOff with unlockID=%s", history.Items, unlockID)
			}
			unlock, active := unlocks.Active("root-1")
			if !active || !unlock.LastUsedAt.Equal(*now) {
				t.Fatalf("earlier completed hop unlock active=%v LastUsedAt=%v, want LastUsedAt=%v", active, unlock.LastUsedAt, *now)
			}
		})
	})

	t.Run("cookie_withhold_continues_and_commits_header_secret_while_header_withhold_refuses", func(t *testing.T) {
		service, _, meta, matcher, _, opened, now := newHeldSetup(t)
		*now = opened.Add(time.Minute)
		var hits atomic.Int32
		var seenAuth, seenCookie atomic.Value
		seenAuth.Store("")
		seenCookie.Store("")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			seenAuth.Store(r.Header.Get("Authorization"))
			seenCookie.Store(r.Header.Get("Cookie"))
			_, _ = io.WriteString(w, "ok")
		}))
		defer server.Close()

		serverURL, err := url.Parse(server.URL)
		testutil.FailErr(t, "parse server URL", err)
		store := httpcookies.New(nil)
		store.SetCookies(serverURL, []*http.Cookie{
			{Name: "session", Value: cookieSecret, Path: "/"},
			{Name: "theme", Value: "dark", Path: "/"},
		})
		jar := &storeBackedCookieJar{store: store}

		args := map[string]any{
			"url":                server.URL + "/api",
			"auth":               map[string]any{"scheme": "bearer", "token": meta.Reference},
			"capability_request": loopbackCapability(t, server.URL),
		}
		deps := Deps{
			Boundary:      testBoundary(),
			Secrets:       service,
			SecretMatcher: matcher,
			SecretAsk: func(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
				if alert.SourcePath == "cookies" {
					return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
				}
				secretcap.ResolutionFrom(ctx).ApproveRelease(secretcap.Release{
					Fingerprints: alert.Fingerprints,
					Recipients:   []secretmatch.Recipient{{ID: alert.DestinationID, Label: alert.DestinationLabel, Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}},
				})
				return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
			},
		}
		resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
			ToolName: "http_request", ToolCallID: "cookie-withhold-header-allow",
		})
		testutil.FailErr(t, "resolve", err)
		tc := sessionContext(t.TempDir(), "cookie-withhold-header-allow")
		tc.Effects.CanonicalArgs = args
		tc.Effects.Secrets = resolved
		ctx := secretcap.WithResolution(t.Context(), resolved)
		_, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{Jar: jar})
		resolved.Finish(t.Context())
		testutil.FailErr(t, "cookie withhold continuation", runErr)

		if hits.Load() != 1 || seenAuth.Load().(string) != "Bearer "+heldSecretVal || seenCookie.Load().(string) != "theme=dark" {
			t.Fatalf("hits=%d auth=%q cookie=%q, want 1, Bearer %s, theme=dark", hits.Load(), seenAuth.Load(), seenCookie.Load(), heldSecretVal)
		}
		history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
		testutil.FailErr(t, "read use history", err)
		if len(history.Items) != 1 || history.Items[0].Delivery != secretcap.DeliveryHandedOff || history.Items[0].UnlockID != unlockID {
			t.Fatalf("cookie withhold header secret history = %+v, want DeliveryHandedOff with unlockID=%s", history.Items, unlockID)
		}

		// Contrast: withholding the header secret itself refuses before dialing and records DeliveryWithheld without touching the unlock.
		service2, _, meta2, matcher2, unlocks2, opened2, now2 := newHeldSetup(t)
		*now2 = opened2.Add(time.Minute)
		hits.Store(0)
		args2 := map[string]any{
			"url":                server.URL + "/api",
			"auth":               map[string]any{"scheme": "bearer", "token": meta2.Reference},
			"capability_request": loopbackCapability(t, server.URL),
		}
		deps2 := Deps{
			Boundary:      testBoundary(),
			Secrets:       service2,
			SecretMatcher: matcher2,
			SecretAsk: func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
				return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
			},
		}
		resolved2, err := service2.Resolve(t.Context(), args2, secretcap.ResolveContext{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
			ToolName: "http_request", ToolCallID: "header-withhold-refuses",
		})
		testutil.FailErr(t, "resolve2", err)
		tc2 := sessionContext(t.TempDir(), "header-withhold-refuses")
		tc2.Effects.CanonicalArgs = args2
		tc2.Effects.Secrets = resolved2
		ctx2 := secretcap.WithResolution(t.Context(), resolved2)
		_, runErr2 := Perform(ctx2, deps2, resolved2.Arguments, tc2, ExchangeOptions{})
		resolved2.Finish(t.Context())
		if reject2 := toolrejection.AsToolReject(runErr2); reject2 == nil || reject2.Code != toolrejection.OutboundSecretDeniedCode {
			t.Fatalf("header withhold err = %v (reject=%+v), want %s", runErr2, reject2, toolrejection.OutboundSecretDeniedCode)
		}
		if hits.Load() != 0 {
			t.Fatalf("header withhold server hits = %d, want 0", hits.Load())
		}
		history2, err := service2.Uses(t.Context(), testdbseed.DefaultProjectID, meta2.Reference, 0)
		testutil.FailErr(t, "read use history2", err)
		if len(history2.Items) != 1 || history2.Items[0].Delivery != secretcap.DeliveryWithheld {
			t.Fatalf("header withhold history = %+v, want DeliveryWithheld", history2.Items)
		}
		if u2, active2 := unlocks2.Active("root-1"); !active2 || !u2.LastUsedAt.Equal(opened2) {
			t.Fatalf("header withhold unlock active=%v LastUsedAt=%v, want %v", active2, u2.LastUsedAt, opened2)
		}
	})

	t.Run("immutable_snapshot_across_rotation_vs_authoritative_capability_revocation_and_classification", func(t *testing.T) {
		t.Run("rotation_during_wait_preserves_frozen_secret_and_token_bytes", func(t *testing.T) {
			service, _, meta, matcher, unlocks, opened, now := newHeldSetup(t)
			*now = opened.Add(time.Minute)
			issuer := issuerServer(t)
			var hits atomic.Int32
			var seenAuth, seenToken atomic.Value
			seenAuth.Store("")
			seenToken.Store("")
			relying := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				seenAuth.Store(r.Header.Get("Authorization"))
				seenToken.Store(r.Header.Get("X-Service-Token"))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer relying.Close()

			root := t.TempDir()
			captureDeps := Deps{Boundary: testBoundary(), Secrets: service, SecretMatcher: matcher}
			captureIssuedToken(t, captureDeps, issuer, root)

			t.Cleanup(func() {
				confine.SetEgressPostureResolver(nil)
				confine.SetApprovalsDisabledSource(nil)
				confine.SetEgressResolver(nil)
			})
			confine.SetApprovalsDisabledSource(func(confine.EgressCommand) bool { return false })
			confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture { return confine.PostureAsk })
			confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
				_, rotErr := service.ReplaceValue(t.Context(), secretcap.ReplaceValueRequest{
					ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "rotated-deploy-token-v2-0000",
				})
				testutil.FailErr(t, "rotate secret during wait", rotErr)
				jarReq := secretcap.TokenJarRequest{
					ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "rot-session", OperationID: "rot-op", Name: "jar",
				}
				openedJar, openErr := service.OpenTokenJar(t.Context(), jarReq)
				testutil.FailErr(t, "open token jar for rotation", openErr)
				openedJar.Set("api", secretcap.Token{Value: "rotated-issued-token-v2", Origin: issuer.URL, OriginLabel: issuer.URL})
				_, saveErr := service.SaveTokenJar(t.Context(), jarReq, openedJar)
				testutil.FailErr(t, "rotate token jar during wait", saveErr)
				return true
			})

			args := map[string]any{
				"url":       relying.URL + "/api",
				"token_jar": "jar",
				"auth":      map[string]any{"scheme": "bearer", "token": meta.Reference},
				"headers": []any{
					map[string]any{"name": "X-Service-Token", "value": "{{token:api}}"},
				},
				"capability_request": loopbackCapability(t, relying.URL),
			}
			asks := 0
			deps := Deps{
				Boundary:      testBoundary(),
				Secrets:       service,
				SecretMatcher: matcher,
				SecretAsk: func(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
					asks++
					secretcap.ResolutionFrom(ctx).ApproveRelease(secretcap.Release{
						Fingerprints: alert.Fingerprints,
						Recipients:   []secretmatch.Recipient{{ID: alert.DestinationID, Label: alert.DestinationLabel, Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}},
					})
					return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
				},
			}
			resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{
				ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
				ToolName: "http_request", ToolCallID: "frozen-snapshot",
			})
			testutil.FailErr(t, "resolve", err)
			tc := sessionContext(root, "frozen-snapshot")
			tc.Effects.CanonicalArgs = args
			tc.Effects.Secrets = resolved
			ctx := secretcap.WithResolution(t.Context(), resolved)
			_, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{})
			resolved.Finish(t.Context())
			testutil.FailErr(t, "perform after rotation", runErr)

			if hits.Load() != 1 || seenAuth.Load().(string) != "Bearer "+heldSecretVal || seenToken.Load().(string) != "issued-token-4411" {
				t.Fatalf("hits=%d auth=%q token=%q, want frozen v1 values", hits.Load(), seenAuth.Load(), seenToken.Load())
			}
			if asks != 2 {
				t.Fatalf("asks = %d, want 2 (1 for request secret, 1 for foreign token, no repeat after wait)", asks)
			}
			history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
			testutil.FailErr(t, "read use history", err)
			if len(history.Items) != 1 || history.Items[0].Delivery != secretcap.DeliveryHandedOff || history.Items[0].Version != 1 || history.Items[0].UnlockID != unlockID {
				t.Fatalf("frozen snapshot use history = %+v, want DeliveryHandedOff v1 with unlockID=%s", history.Items, unlockID)
			}
			unlock, active := unlocks.Active("root-1")
			if !active || !unlock.LastUsedAt.Equal(*now) {
				t.Fatalf("unlock active=%v LastUsedAt=%v, want %v", active, unlock.LastUsedAt, *now)
			}
		})

		t.Run("revoked_secret_or_token_jar_or_withdrawn_classification_during_wait_fails_closed", func(t *testing.T) {
			for _, mode := range []string{"secret_revoked", "token_jar_revoked", "classification_withdrawn"} {
				t.Run(mode, func(t *testing.T) {
					service, owner, meta, matcher, unlocks, opened, now := newHeldSetup(t)
					*now = opened.Add(time.Minute)
					issuer := issuerServer(t)
					var hits atomic.Int32
					relying := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						hits.Add(1)
						w.WriteHeader(http.StatusNoContent)
					}))
					defer relying.Close()

					root := t.TempDir()
					captureDeps := Deps{Boundary: testBoundary(), Secrets: service, SecretMatcher: matcher}
					captureIssuedToken(t, captureDeps, issuer, root)
					openedJar, err := service.OpenTokenJar(t.Context(), secretcap.TokenJarRequest{
						ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "lookup", OperationID: "lookup-op", Name: "jar",
					})
					testutil.FailErr(t, "open seeded token jar", err)

					var ignoreActive atomic.Bool
					ignoreActive.Store(true)
					fp, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("d", 32)))
					testutil.FailErr(t, "fp", err)
					ignoredFP := fp.Fingerprint(plantedBraveKey)
					if mode == "classification_withdrawn" {
						matcher.SetIgnoredSource(func(context.Context) map[secretmatch.SecretFingerprint]bool {
							if ignoreActive.Load() {
								return map[secretmatch.SecretFingerprint]bool{ignoredFP: true}
							}
							return nil
						})
					}

					t.Cleanup(func() {
						confine.SetEgressPostureResolver(nil)
						confine.SetApprovalsDisabledSource(nil)
						confine.SetEgressResolver(nil)
					})
					confine.SetApprovalsDisabledSource(func(confine.EgressCommand) bool { return false })
					confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture { return confine.PostureAsk })
					confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
						switch mode {
						case "secret_revoked":
							_, revErr := service.RevokeProject(t.Context(), testdbseed.DefaultProjectID, meta.Reference, owner)
							testutil.FailErr(t, "revoke secret during wait", revErr)
						case "token_jar_revoked":
							_, revErr := service.RevokeByAgent(t.Context(), testdbseed.DefaultProjectID, "root-1", openedJar.Reference)
							testutil.FailErr(t, "revoke token jar during wait", revErr)
						case "classification_withdrawn":
							ignoreActive.Store(false)
						}
						return true
					})

					headers := []any{map[string]any{"name": "X-Service-Token", "value": "{{token:api}}"}}
					if mode == "classification_withdrawn" {
						headers = append(headers, map[string]any{"name": "X-Subscription-Token", "value": plantedBraveKey})
					}
					args := map[string]any{
						"url":                relying.URL + "/api",
						"token_jar":          "jar",
						"auth":               map[string]any{"scheme": "bearer", "token": meta.Reference},
						"headers":            headers,
						"capability_request": loopbackCapability(t, relying.URL),
					}
					deps := Deps{
						Boundary:      testBoundary(),
						Secrets:       service,
						SecretMatcher: matcher,
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
						ToolName: "http_request", ToolCallID: mode,
					})
					testutil.FailErr(t, "resolve", err)
					tc := sessionContext(root, mode)
					tc.Effects.CanonicalArgs = args
					tc.Effects.Secrets = resolved
					ctx := secretcap.WithResolution(t.Context(), resolved)
					_, runErr := Perform(ctx, deps, resolved.Arguments, tc, ExchangeOptions{})
					resolved.Finish(t.Context())

					reject := toolrejection.AsToolReject(runErr)
					switch mode {
					case "secret_revoked":
						if reject == nil || reject.Code != "SECRET_REFERENCE_REVOKED" {
							t.Fatalf("secret_revoked err = %v (reject=%+v), want SECRET_REFERENCE_REVOKED", runErr, reject)
						}
					case "token_jar_revoked":
						if reject == nil || reject.Code != TokenJarFailedCode {
							t.Fatalf("token_jar_revoked err = %v (reject=%+v), want %s", runErr, reject, TokenJarFailedCode)
						}
					case "classification_withdrawn":
						if reject == nil || reject.Code != toolrejection.OutboundSecretScreenFailedCode || reject.Data["fault_stage"] != secretmatch.FaultStageRaise {
							t.Fatalf("classification_withdrawn err = %v (reject=%+v), want %s (raise)", runErr, reject, toolrejection.OutboundSecretScreenFailedCode)
						}
					}
					if hits.Load() != 0 {
						t.Fatalf("%s server hits = %d, want 0", mode, hits.Load())
					}
					unlock, active := unlocks.Active("root-1")
					if !active || !unlock.LastUsedAt.Equal(opened) {
						t.Fatalf("%s unlock active=%v LastUsedAt=%v, want LastUsedAt=%v", mode, active, unlock.LastUsedAt, opened)
					}
				})
			}
		})
	})
}
