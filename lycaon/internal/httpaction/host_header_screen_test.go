package httpaction

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestHostOverrideScreen(t *testing.T) {
	const value = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
	for _, decision := range []secretmatch.Decision{secretmatch.Withhold, secretmatch.SendRedacted, secretmatch.SendUnchanged} {
		t.Run(string(decision), func(t *testing.T) {
			var seen string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Host
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			asks := 0
			deps := Deps{Boundary: testBoundary(), SecretMatcher: testSecretMatcher(t), SecretAsk: func(_ context.Context, a secretmatch.Alert) (secretmatch.Resolution, error) {
				asks++
				if a.ReviewValue != value {
					t.Errorf("review value = %q", a.ReviewValue)
				}
				return secretmatch.Resolution{Decision: decision}, nil
			}}
			_, err := runRequest(t, deps, map[string]any{
				"url": server.URL, "host_header": value + ".example",
				"capability_request": loopbackCapability(t, server.URL),
			}, sessionContext(t.TempDir(), "host"))
			if asks != 1 {
				t.Fatalf("asks = %d", asks)
			}
			if decision == secretmatch.SendUnchanged {
				testutil.FailErr(t, "approved host override", err)
				if seen != value+".example" {
					t.Fatalf("Host = %q", seen)
				}
			} else {
				var reject *toolrejection.ToolReject
				if !errors.As(err, &reject) {
					t.Fatalf("error = %v", err)
				}
				if seen != "" {
					t.Fatalf("refused request sent Host %q", seen)
				}
				if decision == secretmatch.SendRedacted && reject.Data["fault_stage"] != secretmatch.FaultStageRedactUnsupported {
					t.Fatalf("reject = %+v", reject)
				}
			}
		})
	}
}

func TestCleanHostOverrideWire(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Host; w.WriteHeader(204) }))
	t.Cleanup(server.Close)
	_, err := runRequest(t, Deps{Boundary: testBoundary(), SecretMatcher: testSecretMatcher(t)}, map[string]any{
		"url": server.URL, "host_header": "service.example", "capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "clean"))
	testutil.FailErr(t, "clean host override", err)
	if seen != "service.example" || strings.Contains(seen, "REDACTED") {
		t.Fatalf("Host = %q", seen)
	}
}
