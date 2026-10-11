package httpaction

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// plantedBraveKey exercises a keyword-anchored catalog rule.
const plantedBraveKey = "Zt4Lv8WcY1nJp6HdEs3UbQ2mZx9Rk7Gf"

const braveRuleID = "brave-search-api-key"

func testSecretMatcher(t *testing.T) *secretmatch.Matcher {
	t.Helper()
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher secret-patterns", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("h", 32)))
	testutil.FailErr(t, "build fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)
	return matcher
}

// echoTokenServer records the credential header.
func echoTokenServer(t *testing.T, seen *string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func runTokenRequest(t *testing.T, deps Deps, serverURL string) (result, error) {
	t.Helper()
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register http_request", Register(registry, deps))
	out, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url":                serverURL + "/res/v1/web/search?q=test",
		"headers":            []any{map[string]any{"name": "X-Subscription-Token", "value": plantedBraveKey}},
		"capability_request": loopbackCapability(t, serverURL),
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	})
	if err != nil {
		return result{}, err
	}
	var got result
	testutil.FailErr(t, "decode http response", json.Unmarshal([]byte(out), &got))
	return got, nil
}

func TestSendRedactedRewritesKeywordAnchoredHeader(t *testing.T) {
	var seen string
	server := echoTokenServer(t, &seen)

	var alert secretmatch.Alert
	got, err := runTokenRequest(t, Deps{
		Boundary:      testBoundary(),
		SecretMatcher: testSecretMatcher(t),
		SecretAsk: func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
			alert = finding
			return secretmatch.Resolution{Decision: secretmatch.SendRedacted, ReceiptToken: "receipt-1"}, nil
		},
	}, server.URL)
	testutil.FailErr(t, "run http_request", err)

	if strings.Contains(seen, plantedBraveKey) {
		t.Fatalf("credential reached the destination: %q", seen)
	}
	if seen == "" {
		t.Fatal("header did not reach the destination at all")
	}
	if !got.Redacted || got.ReceiptToken != "receipt-1" {
		t.Fatalf("result redacted=%v receipt=%q", got.Redacted, got.ReceiptToken)
	}
	if alert.RuleID != braveRuleID || len(alert.Fingerprints) != 1 {
		t.Fatalf("alert = %+v", alert)
	}
}

func TestSendUnchangedKeepsHeaderVerbatim(t *testing.T) {
	var seen string
	server := echoTokenServer(t, &seen)

	got, err := runTokenRequest(t, Deps{
		Boundary:      testBoundary(),
		SecretMatcher: testSecretMatcher(t),
		SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
		},
	}, server.URL)
	testutil.FailErr(t, "run http_request", err)

	if seen != plantedBraveKey {
		t.Fatalf("header = %q, want the verbatim value", seen)
	}
	if got.Redacted || got.ReceiptToken != "" {
		t.Fatalf("unchanged send reported redacted=%v receipt=%q", got.Redacted, got.ReceiptToken)
	}
}

func TestWithholdBlocksTheSend(t *testing.T) {
	var seen string
	server := echoTokenServer(t, &seen)

	_, err := runTokenRequest(t, Deps{
		Boundary:      testBoundary(),
		SecretMatcher: testSecretMatcher(t),
		SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
		},
	}, server.URL)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != toolrejection.OutboundSecretDeniedCode {
		t.Fatalf("err = %v, want %s", err, toolrejection.OutboundSecretDeniedCode)
	}
	if seen != "" {
		t.Fatalf("withheld request still dialed with %q", seen)
	}
	for key, value := range reject.Data {
		if strings.Contains(stringifyRejectValue(value), plantedBraveKey) {
			t.Fatalf("reject data %s leaked the value", key)
		}
	}
}

func stringifyRejectValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func TestSecretDestinationBindsTransportNotHostname(t *testing.T) {
	var seen string
	first := echoTokenServer(t, &seen)
	second := echoTokenServer(t, &seen)

	capture := func(server *httptest.Server) secretmatch.Alert {
		var alert secretmatch.Alert
		_, err := runTokenRequest(t, Deps{
			Boundary:      testBoundary(),
			SecretMatcher: testSecretMatcher(t),
			SecretAsk: func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
				alert = finding
				return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
			},
		}, server.URL)
		testutil.FailErr(t, "run http_request", err)
		return alert
	}

	firstAlert, secondAlert := capture(first), capture(second)
	target, err := url.Parse(first.URL)
	testutil.FailErr(t, "parse server url", err)

	if firstAlert.DestinationID == secondAlert.DestinationID {
		t.Fatalf("two ports on one host share a release identity: %q", firstAlert.DestinationID)
	}
	if firstAlert.DestinationID == target.Hostname() {
		t.Fatalf("destination identity is a bare hostname: %q", firstAlert.DestinationID)
	}
	if want := "http://" + strings.ToLower(target.Host); firstAlert.DestinationLabel != want {
		t.Fatalf("destination label = %q, want %q", firstAlert.DestinationLabel, want)
	}
	if secretmatch.HTTPOrigin(target) != firstAlert.DestinationLabel {
		t.Fatalf("label %q does not name the resolved transport", firstAlert.DestinationLabel)
	}
}
