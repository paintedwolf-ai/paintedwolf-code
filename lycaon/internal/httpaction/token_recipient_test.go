package httpaction

import (
	"context"
	"net/netip"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestTokenRecipientOverridesRequireReview(t *testing.T) {
	target, err := url.Parse("https://issuer.example/api")
	testutil.FailErr(t, "parse target", err)
	plain := requestSpec{target: target}
	issuerID, issuerLabel := responseIssuer(plain, outboundhttp.Response{FinalURL: target.String()})
	jar := &secretcap.TokenJar{Name: "jar", Tokens: map[string]secretcap.Token{"api": {Value: "private-token-4411", Origin: issuerID, OriginLabel: issuerLabel}}}
	request := outboundRequest{headers: []outboundhttp.Header{{Name: "Authorization", Value: "Bearer {{token:api}}"}}}
	for name, tc := range map[string]struct {
		spec    requestSpec
		foreign bool
	}{
		"plain":              {plain, false},
		"matching Host":      {requestSpec{target: target, hostHeader: "ISSUER.example:443"}, false},
		"different Host":     {requestSpec{target: target, hostHeader: "other.example"}, true},
		"applicable mapping": {requestSpec{target: target, resolve: []outboundhttp.ResolveMapping{{Host: "issuer.example", Address: netip.MustParseAddr("192.0.2.2")}}}, true},
		"other host mapping": {requestSpec{target: target, resolve: []outboundhttp.ResolveMapping{{Host: "other.example", Address: netip.MustParseAddr("192.0.2.2")}}}, false},
		"other port mapping": {requestSpec{target: target, resolve: []outboundhttp.ResolveMapping{{Host: "issuer.example", Port: 8443, Address: netip.MustParseAddr("192.0.2.2")}}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			placements, reject := placedTokens(request, jar, tc.spec)
			if reject != nil || len(placements) != 1 || placements[0].foreign != tc.foreign {
				t.Fatalf("placements = %+v, reject = %v", placements, reject)
			}
			asks := 0
			deps := Deps{SecretMatcher: testSecretMatcher(t), SecretAsk: func(_ context.Context, a secretmatch.Alert) (secretmatch.Resolution, error) {
				asks++
				if a.Container != "token jar jar" || len(a.SecretNames) != 1 {
					t.Errorf("alert = %+v", a)
				}
				return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
			}}
			_, refusal := releaseForeignTokens(t.Context(), deps, tc.spec, tools.ToolContext{}, jar.Name, placements)
			if tc.foreign && (asks != 1 || refusal == nil) {
				t.Fatalf("asks=%d refusal=%v", asks, refusal)
			}
			if !tc.foreign && (asks != 0 || refusal != nil) {
				t.Fatalf("plain issuer asked: %d %v", asks, refusal)
			}
			capturedID, capturedLabel := responseIssuer(tc.spec, outboundhttp.Response{FinalURL: target.String()})
			captured := &secretcap.TokenJar{Tokens: map[string]secretcap.Token{"api": {Value: "private-token-4411", Origin: capturedID, OriginLabel: capturedLabel}}}
			plainPlacement, reject := placedTokens(request, captured, plain)
			if reject != nil || plainPlacement[0].foreign != tc.foreign {
				t.Fatalf("captured issuer = %q, placement = %+v", capturedID, plainPlacement)
			}
		})
	}
}

func TestTokenSubstitutionPreservesScreenedHost(t *testing.T) {
	request := outboundRequest{url: "https://example.com/", hostHeader: "screened.example", body: []byte("{{token:api}}")}
	result, reject := substituteTokens(request, []tokenPlacement{{name: "api", token: secretcap.Token{Value: "value"}}}, secretmatch.Resolution{})
	if reject != nil || result.hostHeader != request.hostHeader || string(result.body) != "value" {
		t.Fatalf("substitution = %+v, %v", result, reject)
	}
}
