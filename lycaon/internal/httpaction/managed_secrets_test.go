package httpaction

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
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
