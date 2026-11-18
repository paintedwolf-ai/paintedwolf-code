package secretmatch

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestKingfisherRuleScreensOutboundAndScansAtRest(t *testing.T) {
	const ai71Key = "ai71-api-7c12f4e9-4b3d-4a1e-8c72-5d91a6b7c8d9"
	matcher, err := BuildMatcher(Bundled())
	testutil.FailErr(t, "BuildMatcher with Kingfisher", err)
	if hits := matcher.Screen(ai71Key); !hasRule(hits, "kingfisher.ai71.1") {
		t.Fatalf("Kingfisher-only outbound shape did not match: %#v", hits)
	}

	profile, err := BuildScannerProfile(Bundled())
	testutil.FailErr(t, "BuildScannerProfile with Kingfisher", err)
	if _, ok := profile.Config().Rules["kingfisher.ai71.1"]; !ok {
		t.Fatal("at-rest profile is missing Kingfisher rule")
	}
	if _, ok := profile.Config().Rules["kingfisher.jwt.1"]; !ok {
		t.Fatal("outbound-only JWT exclusion must remain active at rest")
	}
}

func TestKingfisherOfflineRequirements(t *testing.T) {
	four := 4
	two := 2
	requirement := kingfisherRequirements{
		MinDigits: &four, MinUppercase: &two, MinLowercase: &two,
		IgnoreIfContains: []string{"example", "placeholder"},
	}
	for _, secret := range []string{"ABcd1234ef", "ZZyy9876xx"} {
		if !requirement.accept(secret) {
			t.Errorf("requirement rejected valid secret %q", secret)
		}
	}
	for _, secret := range []string{"ABcdefgh", "Abcd1234", "ABEXAMPLE1234cd"} {
		if requirement.accept(secret) {
			t.Errorf("requirement accepted invalid secret %q", secret)
		}
	}
}

func TestKingfisherExtendedRegexConversion(t *testing.T) {
	got, err := transpileKingfisherRegex("(?xi) # provider token\n \\b ( sk-test- [A-Z0-9]{4} ) \\b")
	testutil.FailErr(t, "transpile Kingfisher regex", err)
	if got != `(?i)\b(sk-test-[A-Z0-9]{4})\b` {
		t.Fatalf("transpiled regex = %q", got)
	}
	re, err := regexp.Compile(got)
	testutil.FailErr(t, "compile converted regex", err)
	if !re.MatchString("SK-TEST-A1B2") {
		t.Fatal("converted regex did not preserve case-insensitive match")
	}
}

func TestKingfisherKeywordsAreSafeAndBroadlyAvailable(t *testing.T) {
	catalog, err := bundledKingfisherCatalog()
	testutil.FailErr(t, "load Kingfisher catalog", err)
	keyworded := 0
	for _, rule := range catalog.rules {
		if len(rule.keywords) == 0 {
			continue
		}
		keyworded++
	}
	if keyworded < 600 {
		t.Fatalf("only %d/%d Kingfisher rules have a safe required-literal prefilter", keyworded, len(catalog.rules))
	}
	keywords := requiredRegexKeywords(`(?i)provider-(?:live|test)-(TOKEN_[A-Z0-9]+)`)
	for _, keyword := range keywords {
		if !strings.Contains(strings.ToLower("provider-live-TOKEN_AB12"), keyword) ||
			!strings.Contains(strings.ToLower("provider-test-TOKEN_CD34"), keyword) {
			t.Fatalf("derived keyword %q is not required by every alternative", keyword)
		}
	}
}

// A rule's secret is its named secret group, or the first group an alternative fills.
func TestKingfisherCaptureSelectsTheSecretGroup(t *testing.T) {
	catalog, err := bundledKingfisherCatalog()
	testutil.FailErr(t, "load bundled Kingfisher catalog", err)
	const cosmosKey = "oqb4TdY9T0hphvktd5fJnMiHuQqzVy1jd5sSuOpAbGkaoqTlrHl0BOJN2okcasinVLOJzfDbZo1L+ASt68RAhA=="
	for _, tc := range []struct{ rule, text, secret string }{
		{"kingfisher.aws.2", `aws_secret_access_key = 3lyTWqHMt5UySny2drdPYheRTEzrNux8Cn5JWFHL`, "3lyTWqHMt5UySny2drdPYheRTEzrNux8Cn5JWFHL"},
		{"kingfisher.azurecosmosdb.2", "AccountEndpoint=https://acct.documents.azure.com:443/;AccountKey=" + cosmosKey + ";", cosmosKey},
		{"kingfisher.slack.2", "token xoxe-1-" + strings.Repeat("Ab3", 48), "xoxe-1-" + strings.Repeat("Ab3", 48)},
	} {
		rule, ok := catalogRuleByID(catalog.rules, tc.rule)
		if !ok {
			t.Fatalf("%s is not imported", tc.rule)
		}
		if got, ok := rule.capture(tc.text); !ok || got != tc.secret {
			t.Errorf("%s captured %q, want %q", tc.rule, got, tc.secret)
		}
		gitleaks, err := rule.gitleaksRule()
		testutil.FailErr(t, "convert "+tc.rule, err)
		if gitleaks.SecretGroup != max(rule.secretGroup, 0) {
			t.Errorf("%s scanner group = %d, want %d", tc.rule, gitleaks.SecretGroup, rule.secretGroup)
		}
	}
}
