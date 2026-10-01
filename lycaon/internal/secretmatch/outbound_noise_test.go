package secretmatch

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOutboundNoiseRulesRemainAvailableAtRest(t *testing.T) {
	outbound, _, err := buildConfig(outboundProfile, Bundled())
	testutil.FailErr(t, "build outbound secret profile", err)
	scanner, _, err := buildConfig(scannerProfile, Bundled())
	testutil.FailErr(t, "build at-rest secret profile", err)

	for _, id := range []string{
		"algolia-api-key",
		"alibaba-access-key-id",
		"bittrex-access-key",
		"bittrex-secret-key",
		"confluent-access-token",
		"contentful-delivery-api-token",
		"curl-auth-user",
		"mapbox-api-token",
		"kucoin-access-token",
		"twitter-access-token",
		"twitter-api-key",
		"kingfisher.configcat.1",
		"kingfisher.configcat.2",
		"kingfisher.contentful.1",
		"kingfisher.algolia.1",
		"kingfisher.azuresearch.key.1",
		"kingfisher.crossmint.2",
		"kingfisher.customerio.1",
		"kingfisher.devcycle.1",
		"kingfisher.devcycle.2",
		"kingfisher.flagsmith.2",
		"kingfisher.ghost.2",
		"kingfisher.kucoin.1",
		"kingfisher.midtrans.1",
		"kingfisher.midtrans.2",
		"kingfisher.mixpanel.1",
		"kingfisher.mixpanel.3",
		"kingfisher.posthog.1",
		"kingfisher.twitter.4",
	} {
		if _, ok := outbound.Rules[id]; ok {
			t.Errorf("outbound profile retained noisy rule %s", id)
		}
		if _, ok := scanner.Rules[id]; !ok {
			t.Errorf("at-rest profile lost outbound-only exclusion %s", id)
		}
	}
}

func TestOutboundNoiseExamplesDoNotMatch(t *testing.T) {
	matcher := loadBundled(t)
	for name, value := range map[string]string{
		"Algolia search key":        "ALGOLIA_SEARCH_API_KEY=0fff5f614cbcafefb9c1ba319b9905d2",
		"Alibaba access key ID":     "LTAI4G3nD7kQ9sW2xY6zA8bC",
		"Azure Search query key":    "AZURE_SEARCH_QUERY_KEY=XK8TnSRDXsoxiFYiH6Ix2aBC6jvWozd9Ida1yNjWgFHSjeDlblDK",
		"Bittrex access key":        "BITTREX_API_KEY=0fff5f614cbcafefb9c1ba319b9905d2",
		"Confluent API key ID":      "CONFLUENT_API_KEY=ABCD1234EFGH5678",
		"ConfigCat SDK key":         "CONFIGCAT_SDK_KEY=Aa1Bb2Cc3Dd4Ee5Ff6Gg7H/aA1bB2cC3dD4eE5fF6gG7h",
		"Contentful delivery key":   "contentful_delivery_token=wJz-g_tqZ-8n_abcdefghijklmnopqrstuvwxyz12345",
		"Contentful Gitleaks shape": "contentful_token=abcdefghijklmnopqrstuvwxyz0123456789abcdefg",
		"Crossmint client key":      "ck_staging_xK8m2LpQr5nW0vYz3cJ7aB4dE6fG8h",
		"curl basic-auth example":   `curl -u demo-user:demo-pass https://example.test/api`,
		"Customer.io tracking key":  "customerio_tracking_key=f3b0c2b92eca01472efe",
		"DevCycle client key":       "dvc_client_abcdefg1234",
		"DevCycle mobile key":       "dvc_mobile_abcdefg1234",
		"Flagsmith environment key": "FLAGSMITH_ENV_KEY=103edeabc2a901041329b22da61d09cf466cf733",
		"Ghost content key":         "GHOST_CONTENT_API_KEY=22444f78447824223cefc48062",
		"KuCoin API key":            "KUCOIN_API_KEY=4f4ecb6f11b1a70001c8e2ff",
		"Midtrans client key":       "MIDTRANS_CLIENT_KEY=SB-Mid-client-py_WqVmIjA462VHz",
		"Mapbox public token":       "mapbox_token=pk.abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwx.yz0123456789abcdefghij",
		"Mixpanel API key":          "MIXPANEL_API_KEY=0fff5f614cbcafefb9c1ba319b9905d2",
		"PostHog project key":       "phc_E123456789012345678901234567890123456789012",
		"X consumer key":            "TWITTER_API_KEY=4RTBCyG2TbvL407A1lWxQFKCC",
		"X OAuth 1 access token":    "TWITTER_ACCESS_TOKEN=1234567890123456789-aBcDeFgHiJkLmNoPqRsT",
	} {
		if hits := matcher.Screen(value); len(hits) != 0 {
			t.Errorf("%s produced outbound findings: %#v", name, hits)
		}
	}
}

func TestOutboundSensitiveCounterpartsStillMatch(t *testing.T) {
	matcher := loadBundled(t)
	for name, tc := range map[string]struct {
		value  string
		ruleID string
	}{
		"Algolia admin key": {
			value:  "ALGOLIA_ADMIN_API_KEY=0fff5f614cbcafefb9c1ba319b9905d2",
			ruleID: "algolia-admin-api-key",
		},
		"Azure Search admin key": {
			value:  "AZURE_SEARCH_ADMIN_KEY=XK8TnSRDXsoxiFYiH6Ix2aBC6jvWozd9Ida1yNjWgFHSjeDlblDK",
			ruleID: "azure-search-admin-key",
		},
		"Bittrex secret key": {
			value:  "BITTREX_API_SECRET=9f4b2d7e1a3c8056d2e7f1b94a6c3d80",
			ruleID: "bittrex-api-secret",
		},
		"Contentful personal token": {
			value:  "CFPAT-Cq3AarsJCDvdG9PYAJ3Y00crCG5nEPAAfVZ2LAldCsQ",
			ruleID: "kingfisher.contentful.2",
		},
		"Crossmint server key": {
			value:  "CROSSMINT_SECRET_KEY=sk_production_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6",
			ruleID: "kingfisher.crossmint.1",
		},
		"DevCycle server key": {
			value:  "dvc_server_abcdefg1234",
			ruleID: "kingfisher.devcycle.3",
		},
		"Customer.io app key": {
			value:  "customerio_app_key=6e86f5734527548b7477a8b627bf4855",
			ruleID: "kingfisher.customerio.2",
		},
		"Flagsmith organization key": {
			value:  "FLAGSMITH_API_KEY=dVoSZFo3.3c5qN55mA1XdLZnTO2moz5Ud1Qwo18Ie",
			ruleID: "kingfisher.flagsmith.1",
		},
		"Confluent API secret": {
			value:  "CONFLUENT_API_SECRET=cbadefghijklmnopqrstuvwxyzcbaDEFGHIJKLMNOPQRSTUVWXYZ3214567890ab",
			ruleID: "gitleaks:confluent-secret-key",
		},
		"Ghost admin key": {
			value:  "GHOST_ADMIN_API_KEY=1efedd9db174adee2d23d982:4b74dca0219bad629852191af326a45037346c2231240e0f7aec1f9371cc14e8",
			ruleID: "kingfisher.ghost.1",
		},
		"Midtrans server key": {
			value:  "MIDTRANS_SERVER_KEY=SB-Mid-server-Xk93PcDP8pMKfhY2",
			ruleID: "midtrans-server-key",
		},
		"KuCoin API secret": {
			value:  "KUCOIN_API_SECRET=7d70f6c7-42e9-4261-8a8d-8ca2d5028d4f",
			ruleID: "gitleaks:kucoin-secret-key",
		},
		"Mixpanel API secret": {
			value:  "MIXPANEL_API_SECRET=a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
			ruleID: "mixpanel-api-secret",
		},
		"PostHog personal key": {
			value:  "phx_FNKCx83Ko0JQMuZH1zz94xgK798TCUybkf79ZKYKwKQWbEw",
			ruleID: "kingfisher.posthog.2",
		},
		"provider-shaped curl token": {
			value:  "curl -u api:ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3 https://api.github.com/user",
			ruleID: "gitleaks:github-pat",
		},
		"X consumer secret": {
			value:  "TWITTER_API_SECRET=ZGwXeK2DNCqv49Z9ofwYdqlBgeoHDyh8uoAgHju6OeYC7wTQJq",
			ruleID: "gitleaks:twitter-api-secret",
		},
	} {
		hits := matcher.Screen(tc.value)
		if !hasRule(hits, tc.ruleID) {
			t.Errorf("%s: expected %s in %#v", name, tc.ruleID, hits)
		}
	}
}
