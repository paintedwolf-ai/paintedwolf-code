package secretmatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledCatalogInventory(t *testing.T) {
	units, err := loadCatalogUnits(Bundled())
	testutil.FailErr(t, "load bundled catalog", err)
	got := map[string]bool{}
	for _, unit := range units {
		got[unit.ID] = true
	}
	want := []string{
		"adobe-client-id",
		"algolia-admin-api-key",
		"algolia-api-key",
		"alibaba-access-key-id",
		"anthropic-api-key",
		"asana-client-id",
		"authorization-bearer-jwt",
		"aws-access-token",
		"azure-openai-api-key",
		"azure-search-admin-key",
		"bitbucket-client-id",
		"bittrex-access-key",
		"bittrex-api-secret",
		"bittrex-secret-key",
		"brave-search-api-key",
		"contentful-delivery-api-token",
		"confluent-access-token",
		"curl-auth-header",
		"curl-auth-user",
		"discord-client-id",
		"easypost-test-api-token",
		"fireworks-api-key",
		"flutterwave-public-key",
		"gcp-api-key",
		"generic-api-key",
		"github-fine-grained-pat",
		"github-pat",
		"jwt",
		"jwt-base64",
		"kagi-api-key",
		"kucoin-access-token",
		"linkedin-client-id",
		"lob-pub-api-key",
		"looker-client-id",
		"mailgun-pub-key",
		"mapbox-api-token",
		"messagebird-client-id",
		"midtrans-server-key",
		"mixpanel-api-secret",
		"new-relic-browser-api-token",
		"new-relic-user-api-id",
		"openai-api-key",
		"openrouter-api-key",
		"plaid-client-id",
		"private-key",
		"sendbird-access-id",
		"slack-bot-token",
		"sourcegraph-access-token",
		"sumologic-access-id",
		"tavily-api-key",
		"together-api-key",
		"twitter-access-token",
		"twitter-api-key",
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("missing catalog rule %s", id)
		}
	}
	vendor, err := bundledKingfisherCatalog()
	testutil.FailErr(t, "load bundled Kingfisher catalog", err)
	if len(vendor.rules) != 859 {
		t.Errorf("vendor catalog count: got %d want 859", len(vendor.rules))
	}
	if len(got) != len(want) {
		t.Errorf("local catalog count: got %d want %d", len(got), len(want))
	}
}

func TestBuildScannerProfileBundledCatalog(t *testing.T) {
	profile, err := BuildScannerProfile(Bundled())
	testutil.FailErr(t, "BuildScannerProfile", err)
	cfg := profile.Config()
	for _, id := range []string{"generic-api-key", "tavily-api-key"} {
		if _, ok := cfg.Rules[id]; !ok {
			t.Errorf("scanner profile missing %s", id)
		}
	}
}

func TestOutboundExclusionsRemainActiveAtRest(t *testing.T) {
	outbound, _, err := buildConfig(outboundProfile, Bundled())
	testutil.FailErr(t, "build outbound secret profile", err)
	scanner, _, err := buildConfig(scannerProfile, Bundled())
	testutil.FailErr(t, "build at-rest secret profile", err)
	matcher, err := BuildMatcher(Bundled())
	testutil.FailErr(t, "build outbound matcher", err)

	ids := []string{
		"curl-auth-header",
		"sourcegraph-access-token",
		"kingfisher.braintree.1",
		"kingfisher.datadog.4",
		"kingfisher.firebase.2",
		"kingfisher.gcp.1",
		"kingfisher.gcp.3",
		"kingfisher.mapbox.1",
		"kingfisher.pubnub.1",
		"kingfisher.pubnub.2",
		"kingfisher.reactapp.2",
		"kingfisher.sentry.4",
	}
	for _, id := range ids {
		if _, ok := outbound.Rules[id]; ok {
			t.Errorf("outbound profile retained excluded rule %s", id)
		}
		if _, ok := scanner.Rules[id]; !ok {
			t.Errorf("at-rest profile lost outbound-only exclusion %s", id)
		}
	}

	values := map[string]string{
		"curl-auth-header":         `curl -H "Authorization: Bearer your-api-key" https://example.test/v1/search`,
		"sourcegraph-access-token": "sourcegraph documentation describes commits; sha=0123456789abcdef0123456789abcdef01234567",
		"kingfisher.braintree.1":   `BRAINTREE_TOKENIZATION_KEY="sandbox_f252zhq7_hh4cpc39zq4rgjcg"`,
		"kingfisher.datadog.4":     "DATADOG_RUM_TOKEN=pub0123456789abcdef0123456789abcdef",
		"kingfisher.firebase.2":    "FCM_DEVICE_TOKEN=AbCdEfGhIjKlMnOpQrStUv:APA91bZaYxWvUtSrQpOnMlKjIhGfEdCbA9876543210ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-AaBbCcDdEeFfGgHhIiJj",
		"kingfisher.gcp.1":         `{"auth_provider_x509_cert_url":"https://www.googleapis.com/oauth2/v1/certs","client_id":"218284793146200123456"}`,
		"kingfisher.gcp.3":         "gcp_secret = ANzaSy0c3475372a7b10f7740dbda47abfdca42",
		"kingfisher.mapbox.1":      "mapboxApiKey=pk.eyJ1Ijoia3Jpc3R3IiwiYSI6ImNqbGg1N242NTFlczczdnBcf99iMjgzZ2sifQ.lUneM-o3NucXN189EYyXxQ",
		"kingfisher.pubnub.1":      "pub-c-12345678-1234-1234-1234-123456789012",
		"kingfisher.pubnub.2":      "sub-c-12345678-abcd-1234-efgh-567890abcdef",
		"kingfisher.reactapp.2":    "REACT_APP_AUTH_PASSWORD=MyS3cretP@ss",
		"kingfisher.sentry.4":      "SENTRY_DSN=https://0123456789abcdef0123456789abcdef@sentry.io/12345",
	}
	for id, value := range values {
		if hits := matcher.Screen(value); len(hits) != 0 {
			t.Errorf("outbound-only exclusion %s still matched through another rule: %#v", id, hits)
		}
	}
}

func TestBuildScannerProfileIncludesPinnedVendorCatalogWithoutHostLayer(t *testing.T) {
	for _, layers := range [][]Layer{nil, {Dir(filepath.Join(t.TempDir(), "missing"))}, {Dir(t.TempDir())}} {
		profile, err := BuildScannerProfile(layers...)
		testutil.FailErr(t, "BuildScannerProfile vendor catalog", err)
		if _, ok := profile.Config().Rules["kingfisher.groq.1"]; !ok {
			t.Fatalf("BuildScannerProfile(%v) missing pinned vendor rule", layers)
		}
	}
}

func TestDisabledLocalRuleIsOutboundOnlyTuning(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("id: local-token\ntitle: Local token\nregex: 'TOKEN_([A-Za-z0-9_]{8,})'\nsecret_group: 1\nseverity: critical\nenabled: false\n")
	testutil.FailErr(t, "write disabled local rule", os.WriteFile(filepath.Join(dir, "local-token.yaml"), raw, 0o644))

	m, err := BuildMatcher(Dir(dir))
	testutil.FailErr(t, "BuildMatcher disabled local rule", err)
	if hits := m.Screen("TOKEN_aB3dE5fG7hJ9"); len(hits) != 0 {
		t.Fatalf("disabled local rule matched outbound: %#v", hits)
	}
	profile, err := BuildScannerProfile(Dir(dir))
	testutil.FailErr(t, "BuildScannerProfile disabled local rule", err)
	if _, ok := profile.Config().Rules["local-token"]; !ok {
		t.Fatal("at-rest profile must retain a locally defined rule disabled only for outbound screening")
	}
}

func TestBuildMatcherLaterDirOverrides(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("id: aws-access-token\nupstream_id: aws-access-token\ntitle: Override\nseverity: high\n")
	testutil.FailErr(t, "write override", os.WriteFile(filepath.Join(dir, "aws-access-token.yaml"), raw, 0o644))
	m, err := BuildMatcher(Bundled(), Dir(dir))
	testutil.FailErr(t, "BuildMatcher merge", err)
	hits := m.Screen(plantAWS)
	if len(hits) == 0 || hits[0].Title != "Override" || hits[0].Severity != "high" {
		t.Fatalf("screen override: %#v", hits)
	}
}

func TestBuildMatcherLocalStopwordAllowlist(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("id: local-token\ntitle: Local token\nregex: 'TOKEN_([A-Za-z0-9_]{8,})'\nsecret_group: 1\nseverity: critical\nallowlist_stopwords: [EXAMPLE_VALUE]\n")
	testutil.FailErr(t, "write local rule", os.WriteFile(filepath.Join(dir, "local-token.yaml"), raw, 0o644))
	m, err := BuildMatcher(Dir(dir))
	testutil.FailErr(t, "BuildMatcher local stopword allowlist", err)
	if hits := m.Screen("TOKEN_EXAMPLE_VALUE"); len(hits) != 0 {
		t.Fatalf("documented placeholder matched: %#v", hits)
	}
	if hits := m.Screen("TOKEN_aB3dE5fG7hJ9"); !hasRule(hits, "local-token") {
		t.Fatalf("realistic local token did not match: %#v", hits)
	}
}

func TestBuildMatcherUpstreamStopwordAllowlist(t *testing.T) {
	const awsCanary = "AKIAQYJK5TXV4NCANARY"
	baseline, err := BuildMatcher(Dir(filepath.Join(t.TempDir(), "missing")))
	testutil.FailErr(t, "BuildMatcher baseline", err)
	if !hasRule(baseline.Screen(awsCanary), "gitleaks:aws-access-token") {
		t.Fatal("upstream canary did not match before overlay allowlist")
	}

	dir := t.TempDir()
	raw := []byte("id: aws-access-token\nupstream_id: aws-access-token\nallowlist_stopwords: [CANARY]\n")
	testutil.FailErr(t, "write upstream allowlist", os.WriteFile(filepath.Join(dir, "aws-access-token.yaml"), raw, 0o644))
	m, err := BuildMatcher(Dir(dir))
	testutil.FailErr(t, "BuildMatcher upstream allowlist", err)
	if hits := m.Screen(awsCanary); len(hits) != 0 {
		t.Fatalf("upstream placeholder matched: %#v", hits)
	}

	profile, err := BuildScannerProfile(Dir(dir))
	testutil.FailErr(t, "BuildScannerProfile upstream allowlist", err)
	rule := profile.Config().Rules["aws-access-token"]
	allowed := false
	for _, allowlist := range rule.Allowlists {
		if ok, _ := allowlist.ContainsStopWord(awsCanary); ok {
			allowed = true
			break
		}
	}
	if !allowed {
		t.Fatal("scanner profile did not inherit upstream placeholder allowlist")
	}
}

func TestBuildMatcherRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing-id.yaml",
			body: "upstream_id: aws-access-token\ntitle: X\nseverity: high\n",
			want: "missing id",
		},
		{
			name: "bad-sev.yaml",
			body: "id: bad-sev\ntitle: X\nregex: 'x'\nseverity: low\n",
			want: "severity",
		},
		{
			name: "both-kinds.yaml",
			body: "id: both-kinds\nupstream_id: aws-access-token\nregex: 'a'\nseverity: high\ntitle: X\n",
			want: "both upstream_id and regex",
		},
		{
			name: "aws-access-token.yaml",
			body: "id: aws-access-token\ntitle: Replacement\nregex: 'TOKEN_[A-Za-z0-9]+'\nseverity: high\n",
			want: "collides with an upstream rule",
		},
		{
			name: "id-mismatch.yaml",
			body: "id: other-id\nupstream_id: aws-access-token\ntitle: X\nseverity: high\n",
			want: "must match filename stem",
		},
		{
			name: "mismatched-upstream.yaml",
			body: "id: mismatched-upstream\nupstream_id: aws-access-token\n",
			want: "must match upstream_id",
		},
		{
			name: "bad-regex.yaml",
			body: "id: bad-regex\ntitle: X\nregex: '('\nseverity: high\n",
			want: "bad regex",
		},
		{
			name: "unknown-field.yaml",
			body: "id: unknown-field\nupstream_id: aws-access-token\nallowlist_stopword: [EXAMPLE]\n",
			want: "field allowlist_stopword not found",
		},
		{
			name: "multiple-documents.yaml",
			body: "id: multiple-documents\nupstream_id: aws-access-token\n---\nid: ignored\n",
			want: "multiple YAML documents are not supported",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.name)
			testutil.FailErr(t, "write", os.WriteFile(path, []byte(tc.body), 0o644))
			_, err := BuildMatcher(Dir(dir))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("error should name file: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestBuildMatcherMissingDirStillActive(t *testing.T) {
	m, err := BuildMatcher(Dir(filepath.Join(t.TempDir(), "does-not-exist")))
	testutil.FailErr(t, "BuildMatcher missing", err)
	if m.Inert() {
		t.Fatal("missing catalog dir must still use the compiled floor")
	}
	if !hasRule(m.Screen(plantAWS), "gitleaks:aws-access-token") {
		t.Fatal("floor should still match AWS canary")
	}
}
