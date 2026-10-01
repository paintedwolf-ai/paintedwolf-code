package secretmatch

import (
	"bufio"
	"encoding/base64"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Planted values satisfy the compiled rule constraints.

const (
	plantAWS        = "AKIAQYJK5TXV4NZR7SGB"
	plantGitHub     = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
	plantGitHubFG   = "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"
	plantSlack      = "xoxb-1234567890-1234567890123-abcdefghijklmnopqrstuvwx"
	plantGCP        = "AIzaOiEmzfFABGo571rwWK6vNEH2n3gMXC7rRg2"
	plantOpenAI     = "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"
	plantAnthropic  = "sk-ant-api03-MeMT3IrWiPZ_35Kvc8kYJV10RQgM0xJdJNruAhMZl01h_l3qqkaUyEhVgS1Snh7YWAgZbdM_93OA5mJtTzaCrSobe0EFnAA"
	plantJWT        = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	plantPEM        = "-----BEGIN RSA PRIVATE KEY-----\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END RSA PRIVATE KEY-----"
	plantGeneric    = "token=TaV6bX5gNHWEAQrDqpHDfIU0TbqorF1eDEjZQYYY5FaWYUBz"
	plantTavily     = "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"
	plantCloudflare = "cloudflare_api_key=Bu0rrK-lerk6y0Suqo1qSqlDDajOk61wZchCkje4"
	plantOpenRouter = "sk-or-v1-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	plantFireworks  = "fw_aB3dE5fG7hJ9kL2mN4pQ6rS8tV0xY1zC"
	plantTogether   = "Tg3dE5fG7hJ9kL2mN4pQ6rS8tV0xY1zC"
	plantBrave      = "Br3dE5fG7hJ9kL2mN4pQ6rS8tV0xY1zC"
	plantKagi       = "Kg3dE5fG7hJ9kL2mN4pQ6rS8tV0xY1zC"
	plantAzure      = "Az3dE5fG7hJ9kL2mN4pQ6rS8tV0xY1zC"
)

func loadBundled(t *testing.T) *Matcher {
	t.Helper()
	m, err := BuildMatcher(Bundled())
	testutil.FailErr(t, "BuildMatcher bundled secret-patterns", err)
	if m.Inert() {
		t.Fatal("expected non-inert bundled matcher")
	}
	return m
}

func TestScreenDetectsEveryFamilyWithExpectedRuleID(t *testing.T) {
	m := loadBundled(t)
	cases := []struct {
		value  string
		ruleID string
	}{
		{plantAWS, "gitleaks:aws-access-token"},
		{plantGitHub, "gitleaks:github-pat"},
		{plantGitHubFG, "gitleaks:github-fine-grained-pat"},
		{plantSlack, "gitleaks:slack-bot-token"},
		{plantGCP, "gitleaks:gcp-api-key"},
		{plantOpenAI, "gitleaks:openai-api-key"},
		{plantAnthropic, "gitleaks:anthropic-api-key"},
		{"Authorization: Bearer " + plantJWT, "authorization-bearer-jwt"},
		{plantPEM, "gitleaks:private-key"},
		{plantTavily, "tavily-api-key"},
		{plantCloudflare, "gitleaks:cloudflare-api-key"},
		{plantOpenRouter, "openrouter-api-key"},
		{plantFireworks, "fireworks-api-key"},
		{"TOGETHER_API_KEY=" + plantTogether, "together-api-key"},
		{"X-Subscription-Token: " + plantBrave, "brave-search-api-key"},
		{"KAGI_API_KEY=" + plantKagi, "kagi-api-key"},
		{"AZURE_OPENAI_API_KEY=" + plantAzure, "azure-openai-api-key"},
	}
	for _, tc := range cases {
		hits := m.Screen(tc.value)
		if !hasRule(hits, tc.ruleID) {
			t.Errorf("%s: expected %s in %#v", tc.ruleID, tc.ruleID, hits)
		}
	}
}

func TestProviderLabelRulesUseStructuredContextOnly(t *testing.T) {
	m := loadBundled(t)
	cases := []struct {
		label  string
		value  string
		ruleID string
	}{
		{"TOGETHER_API_KEY", plantTogether, "together-api-key"},
		{"BRAVE_SEARCH_API_KEY", plantBrave, "brave-search-api-key"},
		{"KAGI_API_KEY", plantKagi, "kagi-api-key"},
		{"AZURE_OPENAI_API_KEY", plantAzure, "azure-openai-api-key"},
	}
	for _, tc := range cases {
		if hits := m.Screen(tc.value); len(hits) != 0 {
			t.Errorf("%s bare value matched without provider context: %#v", tc.ruleID, hits)
		}
		if hits := m.ScreenLabeledContext(t.Context(), tc.label, tc.value); !hasRule(hits, tc.ruleID) {
			t.Errorf("%s structured label did not match: %#v", tc.ruleID, hits)
		}
	}
	// Contextual rules require their exact field labels.
	for _, value := range []string{
		"API_KEY=" + plantTogether,
		"token=" + plantTogether,
		"https://example.com/?api_key=" + plantTogether,
		"KELLNR_REGISTRY__AUTH_TOKEN=registry-token-demo",
	} {
		if hits := m.Screen(value); len(hits) != 0 {
			t.Errorf("generic credential context matched without a provider shape: %q -> %#v", value, hits)
		}
	}
}

func TestScreenNegativesStillClean(t *testing.T) {
	m := loadBundled(t)
	lines := goldenLines(t, "negatives.txt")
	for _, line := range lines {
		if hits := m.Screen(line); len(hits) != 0 {
			t.Errorf("unexpected match for %q: %#v", line, hits)
		}
	}
}

func TestPlainAndEncodedJWTStayCleanWithoutAuthorizationContext(t *testing.T) {
	m := loadBundled(t)
	values := []string{
		plantJWT,
		"jwt=" + plantJWT,
		base64.StdEncoding.EncodeToString([]byte(plantJWT)),
		"Bearer " + plantJWT,
	}
	for _, value := range values {
		if hits := m.Screen(value); len(hits) != 0 {
			t.Errorf("plain JWT form matched: %#v", hits)
		}
	}
}

func TestPublicIdentifiersAndTestTokensAreOutboundClean(t *testing.T) {
	stock := loadBundled(t)
	upstream, err := BuildMatcher(Dir(filepath.Join(t.TempDir(), "missing")))
	testutil.FailErr(t, "BuildMatcher upstream floor", err)
	cases := []struct {
		value  string
		ruleID string
	}{
		{"adobe_client_id=1234567890abcdef1234567890abcdef", "gitleaks:adobe-client-id"},
		{"asana_client_id=1234567890123456", "gitleaks:asana-client-id"},
		{"bitbucket_client_id=abcdef0123456789abcdef0123456789", "gitleaks:bitbucket-client-id"},
		{"discord_client_id=123456789012345678", "gitleaks:discord-client-id"},
		{"EZTKGqQRiRyqAFRQshGPWOIAwNWGORfKHSBnVNFtVmWYoW6PH23lkqbbDW", "gitleaks:easypost-test-api-token"},
		{"FLWPUBK_TEST-abcdefgh01234567abcdefgh01234567-X", "gitleaks:flutterwave-public-key"},
		{"linkedin_client_id=ab12cd34ef56gh", "gitleaks:linkedin-client-id"},
		{"lob_key=test_pub_abcdef0123456789abcdef012345678", "gitleaks:lob-pub-api-key"},
		{"looker_client_id=ab12cd34ef56gh78ij90", "gitleaks:looker-client-id"},
		{"mailgun_key=pubkey-abcdef0123456789abcdef0123456789", "gitleaks:mailgun-pub-key"},
		{"messagebird_client_id=12345678-1234-4abc-8def-1234567890ab", "gitleaks:messagebird-client-id"},
		{"new_relic_browser=NRJS-1234567890abcdef123", "gitleaks:new-relic-browser-api-token"},
		{"new_relic_user_id=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "gitleaks:new-relic-user-api-id"},
		{"plaid_client_id=ab12cd34ef56gh78ij90klmn", "gitleaks:plaid-client-id"},
		{"sendbird_access_id=12345678-1234-4abc-8def-1234567890ab", "gitleaks:sendbird-access-id"},
		{"SUMO_ACCESS_ID=suAb3dE5fG7hJ9", "gitleaks:sumologic-access-id"},
	}
	for _, tc := range cases {
		if !hasRule(upstream.Screen(tc.value), tc.ruleID) {
			t.Errorf("invalid upstream plant for %s", tc.ruleID)
			continue
		}
		if hits := stock.Screen(tc.value); len(hits) != 0 {
			t.Errorf("%s should be outbound-clean: %#v", tc.ruleID, hits)
		}
	}
}

func TestUpstreamAllowlistedValuesDoNotMatch(t *testing.T) {
	m := loadBundled(t)
	// One fixture is allowlisted and one has an invalid suffix.
	for _, v := range []string{"AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7CANARY0"} {
		if hits := m.Screen(v); len(hits) != 0 {
			t.Errorf("%q must not match (allowlisted/invalid); got %#v", v, hits)
		}
	}
}

func TestMatchNeverContainsValue(t *testing.T) {
	m := loadBundled(t)
	values := []string{plantAWS, plantGitHub, plantOpenAI, plantAnthropic, "Authorization: Bearer " + plantJWT}
	for _, value := range values {
		hits := m.Screen(value)
		if len(hits) == 0 {
			t.Fatalf("expected match for %q", value)
		}
		for _, hit := range hits {
			rt := reflect.TypeOf(hit)
			rv := reflect.ValueOf(hit)
			for i := 0; i < rt.NumField(); i++ {
				f := rv.Field(i)
				if f.Kind() != reflect.String {
					continue
				}
				if strings.Contains(f.String(), value) {
					t.Errorf("Match.%s contains full value", rt.Field(i).Name)
				}
			}
			runes := []rune(value)
			if hit.Start < 0 || hit.End > len(runes) || hit.Start >= hit.End {
				// Multiline matches still use valid rune offsets.
				screened := []rune(value)
				if hit.Start < 0 || hit.End > len(screened) || hit.Start >= hit.End {
					t.Fatalf("bad offsets %#v on %q", hit, value)
				}
			}
		}
	}
}

func TestRedactStringDropsTheEntireMatchedValue(t *testing.T) {
	m := loadBundled(t)
	in := "credential=" + plantAWS + " remains private"
	out := m.RedactString(t.Context(), in)
	if strings.Contains(out, plantAWS) {
		t.Fatalf("redaction leaked secret: %s", out)
	}
	if !strings.Contains(out, "credential=[REDACTED]") || !strings.Contains(out, "remains private") {
		t.Fatalf("redaction lost surrounding facts: %s", out)
	}
}

func TestRedactStringUsesOffsetOrderAcrossSeverities(t *testing.T) {
	m := loadBundled(t)
	in := "Authorization: Bearer " + plantJWT + "\naws=" + plantAWS
	out := m.RedactString(t.Context(), in)
	if strings.Contains(out, plantJWT) || strings.Contains(out, plantAWS) {
		t.Fatalf("redaction leaked a mixed-severity secret: %s", out)
	}
	if strings.Count(out, "[REDACTED]") != 2 || !strings.Contains(out, "Authorization: Bearer [REDACTED]\naws=[REDACTED]") {
		t.Fatalf("redaction corrupted surrounding content: %s", out)
	}
}

func TestFindingNeverEscapes(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	testutil.FailErr(t, "read secretmatch dir", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, 0)
		testutil.FailErr(t, "parse "+e.Name(), err)
		base := e.Name()
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				return false
			}
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Name == nil || !x.Name.IsExported() || x.Type == nil {
					return true
				}
				checkExportedTypeExpr(t, base, x.Name.Name, x.Type.Results)
				checkExportedTypeExpr(t, base, x.Name.Name, x.Type.Params)
			case *ast.TypeSpec:
				if x.Name == nil || !x.Name.IsExported() {
					return true
				}
				if x.Name.Name == "Match" {
					st, ok := x.Type.(*ast.StructType)
					if !ok {
						return true
					}
					for _, f := range st.Fields.List {
						for _, name := range f.Names {
							switch name.Name {
							case "Secret", "Match", "Line", "Fragment", "Value":
								t.Errorf("Match must not have field %s", name.Name)
							}
						}
					}
				}
				checkExportedTypeExpr(t, base, x.Name.Name, x.Type)
			}
			return true
		})
	}
}

func checkExportedTypeExpr(t *testing.T, file, typeName string, node ast.Node) {
	t.Helper()
	if node == nil {
		return
	}
	if fields, ok := node.(*ast.FieldList); ok && fields == nil {
		return
	}
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == "report" && sel.Sel.Name == "Finding" {
			t.Errorf("%s: exported %s must not mention report.Finding", file, typeName)
		}
		return true
	})
}

func TestInertMatcherByteIdentical(t *testing.T) {
	for _, m := range []*Matcher{nil, NewInertMatcher()} {
		if !m.Inert() {
			t.Fatal("expected Inert")
		}
		if hits := m.Screen(plantAWS); hits != nil {
			t.Fatalf("inert Screen: %#v", hits)
		}
	}
}

func TestTuningUnitDisablesUpstreamRule(t *testing.T) {
	m := loadBundled(t)
	if hits := m.Screen(plantGeneric); len(hits) != 0 {
		t.Fatalf("generic-api-key should be disabled; got %#v", hits)
	}

	dir := t.TempDir()
	raw := []byte("id: generic-api-key\nupstream_id: generic-api-key\nenabled: true\n")
	testutil.FailErr(t, "write enable", os.WriteFile(filepath.Join(dir, "generic-api-key.yaml"), raw, 0o644))
	enabled, err := BuildMatcher(Dir(dir))
	testutil.FailErr(t, "BuildMatcher enable generic", err)
	if !hasRule(enabled.Screen(plantGeneric), "gitleaks:generic-api-key") {
		t.Fatalf("enabled generic-api-key should match; got %#v", enabled.Screen(plantGeneric))
	}
}

func TestTuningUnitUnknownUpstreamIDFails(t *testing.T) {
	dir := t.TempDir()
	name := "no-such-rule.yaml"
	raw := []byte("id: no-such-rule\nupstream_id: no-such-rule\ntitle: X\nseverity: high\n")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, name), raw, 0o644))
	_, err := BuildMatcher(Dir(dir))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "no-such-rule") {
		t.Fatalf("error should name file and id: %v", err)
	}
}

func TestTuningUnitTitleWinsOverDescription(t *testing.T) {
	m := loadBundled(t)
	hits := m.Screen(plantAWS)
	if len(hits) == 0 || hits[0].RuleID != "gitleaks:aws-access-token" {
		t.Fatalf("got %#v", hits)
	}
	if hits[0].Title != "AWS access key ID" {
		t.Fatalf("title=%q want curated", hits[0].Title)
	}

	// Untuned rules use their catalog description.
	untuned, err := BuildMatcher(Dir(t.TempDir())) // empty overlay
	testutil.FailErr(t, "BuildMatcher empty catalog", err)
	hits = untuned.Screen(plantAWS)
	if len(hits) == 0 {
		t.Fatal("expected match")
	}
	if hits[0].Title == "AWS access key ID" || hits[0].Title == "" {
		t.Fatalf("untuned title should be upstream description, got %q", hits[0].Title)
	}
	if !strings.Contains(strings.ToLower(hits[0].Title), "aws") {
		t.Fatalf("unexpected description %q", hits[0].Title)
	}
}

func TestLocalRuleUnitStillWorks(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("id: local-canary\ntitle: Local canary\nregex: 'LOCALCANARY[0-9]{8}'\nseverity: high\nkeywords:\n  - localcanary\n")
	testutil.FailErr(t, "write local", os.WriteFile(filepath.Join(dir, "local-canary.yaml"), raw, 0o644))
	m, err := BuildMatcher(Dir(dir))
	testutil.FailErr(t, "BuildMatcher local", err)
	hits := m.Screen("prefix LOCALCANARY12345678 suffix")
	if !hasRule(hits, "local-canary") {
		t.Fatalf("want local-canary, got %#v", hits)
	}
	if strings.HasPrefix(hits[0].RuleID, "gitleaks:") {
		t.Fatalf("local rule must stay bare: %s", hits[0].RuleID)
	}
}

func TestSeverityDefaultsCritical(t *testing.T) {
	untuned, err := BuildMatcher(Dir(filepath.Join(t.TempDir(), "missing")))
	testutil.FailErr(t, "BuildMatcher", err)
	hits := untuned.Screen(plantAWS)
	if len(hits) == 0 || hits[0].Severity != "critical" {
		t.Fatalf("default severity: %#v", hits)
	}

	dir := t.TempDir()
	raw := []byte("id: aws-access-token\nupstream_id: aws-access-token\nseverity: high\n")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "aws-access-token.yaml"), raw, 0o644))
	m, err := BuildMatcher(Dir(dir))
	testutil.FailErr(t, "BuildMatcher high", err)
	hits = m.Screen(plantAWS + " " + plantJWT)
	if len(hits) < 2 {
		t.Fatalf("want both matches, got %#v", hits)
	}
	// Critical findings sort before high findings.
	if hits[0].Severity != "critical" {
		t.Fatalf("ms[0] severity=%s want critical first: %#v", hits[0].Severity, hits)
	}
}

func TestBase64WrappedSecretDetected(t *testing.T) {
	m := loadBundled(t)
	l1 := base64.StdEncoding.EncodeToString([]byte(plantAWS))
	l2 := base64.StdEncoding.EncodeToString([]byte(l1))
	l3 := base64.StdEncoding.EncodeToString([]byte(l2))
	if !hasRule(m.Screen("q="+l1), "gitleaks:aws-access-token") {
		t.Fatal("depth-1 base64 should match")
	}
	if !hasRule(m.Screen("q="+l2), "gitleaks:aws-access-token") {
		t.Fatal("depth-2 base64 should match (MaxDecodeDepth=2)")
	}
	if hits := m.Screen("q=" + l3); hasRule(hits, "gitleaks:aws-access-token") {
		t.Fatalf("depth-3 must not match under MaxDecodeDepth=2; got %#v", hits)
	}
}

func TestOverlapDedupAcrossUpstreamRules(t *testing.T) {
	m := loadBundled(t)
	hits := m.Screen(plantGitHubFG)
	var ids []string
	for _, h := range hits {
		ids = append(ids, h.RuleID)
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one Match, got %v", ids)
	}
	if hits[0].RuleID != "gitleaks:github-fine-grained-pat" {
		t.Fatalf("want github-fine-grained-pat, got %v", ids)
	}
}

func TestMatcherBuiltOnceAtBoot(t *testing.T) {
	m := loadBundled(t)
	d1 := m.detector
	_ = m.Screen(plantAWS)
	_ = m.Screen(plantGitHub)
	if m.detector != d1 {
		t.Fatal("Screen must not rebuild the detector")
	}
}

func BenchmarkScreenShortQuery(b *testing.B) {
	m, err := BuildMatcher(Bundled())
	if err != nil {
		b.Fatal(err)
	}
	q := "search docs about " + plantAWS + " and related APIs"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Screen(q)
	}
}

func goldenLines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	testutil.FailErr(t, "open "+name, err)
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	testutil.FailErr(t, "scan "+name, sc.Err())
	if len(lines) == 0 {
		t.Fatalf("%s: no golden lines", name)
	}
	return lines
}

func hasRule(matches []Match, ruleID string) bool {
	for _, m := range matches {
		if m.RuleID == ruleID {
			return true
		}
	}
	return false
}

func TestGenericShapeDescribesClassesWithoutMatchedCharacters(t *testing.T) {
	want := "abcab-123 (9 characters)"
	if got := GenericShape("alpha-123"); got != want {
		t.Fatalf("shape = %q, want %q", got, want)
	}
	if first, second := GenericShape("abc_DEF123"), GenericShape("xyz+QWE789"); first != second {
		t.Fatalf("same generic structure produced distinct shapes: %q != %q", first, second)
	}
	shape := GenericShape(plantGitHub)
	if strings.Contains(shape, "ghp_") || strings.Contains(shape, plantGitHub) {
		t.Fatalf("generic shape leaked matched characters: %q", shape)
	}
	if !strings.HasPrefix(shape, "abc-a1b2c3") {
		t.Fatalf("generic shape is not a concrete synthetic example: %q", shape)
	}
}

func TestMatcherCarriesGenericShape(t *testing.T) {
	m := loadBundled(t)
	hits := m.Screen(plantGitHub)
	if len(hits) == 0 || hits[0].GenericShape == "" {
		t.Fatalf("missing generic shape: %#v", hits)
	}
}

func TestGenericShapeBoundsLongExamples(t *testing.T) {
	shape := GenericShape(strings.Repeat("Z", 200))
	if !strings.Contains(shape, "… (200 characters)") {
		t.Fatalf("long generic shape was not bounded: %q", shape)
	}
	if utf8.RuneCountInString(shape) > genericShapePreviewRunes+20 {
		t.Fatalf("long generic shape is too large: %d runes", utf8.RuneCountInString(shape))
	}
}

// filler builds line-aligned screen-neutral padding.
func filler(n int) string {
	line := "the quick brown fox jumps over the lazy dog and carries nothing secret\n"
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(line)
	}
	return b.String()
}

// Narrow windows keep boundary cases inexpensive.
const (
	testWindowBytes  = 8 << 10
	testOverlapBytes = 1 << 10
)

// narrowWindowMatcher is a bundled matcher that screens in small windows.
func narrowWindowMatcher(t *testing.T) *Matcher {
	t.Helper()
	m := loadBundled(t)
	m.SetScreenWindow(testWindowBytes, testOverlapBytes)
	return m
}

func TestScreenCoversInputBeyondOneWindow(t *testing.T) {
	m := narrowWindowMatcher(t)
	// Place the credential beyond the first window.
	body := filler(testWindowBytes+512) + "aws_key = " + plantAWS + "\n"
	hits := m.Screen(body)
	if len(hits) != 1 {
		t.Fatalf("hits = %d want 1", len(hits))
	}
	if !strings.Contains(hits[0].RuleID, "aws") {
		t.Fatalf("rule = %q", hits[0].RuleID)
	}
	if got := string([]rune(body)[hits[0].Start:hits[0].End]); got != plantAWS {
		t.Fatalf("span = %q want the planted value", got)
	}
}

func TestScreenReportsWindowStraddlingSecretOnce(t *testing.T) {
	m := narrowWindowMatcher(t)
	// Place the credential in the repeated overlap.
	head := filler(testWindowBytes - (testOverlapBytes / 2))
	body := head + "aws_key = " + plantAWS + "\n" + filler(2*testWindowBytes)
	hits := m.Screen(body)
	if len(hits) != 1 {
		t.Fatalf("hits = %d want 1 (overlap must not double-report)", len(hits))
	}
	if got := string([]rune(body)[hits[0].Start:hits[0].End]); got != plantAWS {
		t.Fatalf("span = %q want the planted value", got)
	}
}

func TestRedactStringReplacesEverySpanAcrossWindows(t *testing.T) {
	m := narrowWindowMatcher(t)
	body := "aws_key = " + plantAWS + "\n" + filler(testWindowBytes+512) + "gh = " + plantGitHub + "\n"
	out := m.RedactString(t.Context(), body)
	if strings.Contains(out, plantAWS) || strings.Contains(out, plantGitHub) {
		t.Fatal("redacted output still carries a planted value")
	}
	if got := strings.Count(out, "[REDACTED]"); got != 2 {
		t.Fatalf("[REDACTED] count = %d want 2", got)
	}
}

func TestScreenWalksTheShippedWindow(t *testing.T) {
	testutil.SkipIfShort(t, "scans past the shipped 1 MiB window")
	m := loadBundled(t)
	body := filler(MaxScreenWindowBytes+8<<10) + "aws_key = " + plantAWS + "\n"
	hits := m.Screen(body)
	if len(hits) != 1 {
		t.Fatalf("hits = %d want 1 past the shipped window", len(hits))
	}
	if got := string([]rune(body)[hits[0].Start:hits[0].End]); got != plantAWS {
		t.Fatalf("span = %q want the planted value", got)
	}
}

func TestScreenWindowDefaultsHold(t *testing.T) {
	m := loadBundled(t)
	if got := m.screenWindowBytes(); got != MaxScreenWindowBytes {
		t.Fatalf("default window = %d want %d", got, MaxScreenWindowBytes)
	}
	if got := m.screenOverlap(); got != screenWindowOverlapBytes {
		t.Fatalf("default overlap = %d want %d", got, screenWindowOverlapBytes)
	}
	m.SetScreenWindow(testWindowBytes, testOverlapBytes)
	m.SetScreenWindow(0, 0)
	if m.screenWindowBytes() != MaxScreenWindowBytes || m.screenOverlap() != screenWindowOverlapBytes {
		t.Fatal("zero must restore the shipped defaults")
	}
}

func TestRedactLabeledSpansMatchSubstitutionsApplied(t *testing.T) {
	m := loadBundled(t)
	var b strings.Builder
	for i := range 40 {
		b.WriteString("line " + strconv.Itoa(i) + " aws_key = " + plantAWS + "\n")
	}
	out, spans := m.RedactLabeledSpansWhere(t.Context(), "", b.String(), nil)
	count := len(spans)
	if strings.Contains(out, plantAWS) {
		t.Fatal("redacted output still carries a planted value")
	}
	if got := strings.Count(out, "[REDACTED]"); got != count {
		t.Fatalf("count = %d but applied %d substitutions", count, got)
	}
	if count != 40 {
		t.Fatalf("count = %d want 40", count)
	}
	// Preserve text surrounding replacements.
	if !strings.Contains(out, "line 39 aws_key = [REDACTED]") {
		t.Fatalf("surrounding text lost: %q", out[max(0, len(out)-80):])
	}
}

// Overlapping matches redact their union while retaining one attribution.
func TestRedactMatchesMasksUnionOfOverlappingSpans(t *testing.T) {
	// The lower-ranked match extends beyond the attribution winner.
	s := "prefix SECRETVALUE suffix"
	start := len([]rune("prefix "))
	winner := Match{RuleID: "vendor.short", Severity: "high", Start: start, End: start + len([]rune("SECRET"))}
	loser := Match{RuleID: "generic.long", Severity: "medium", Start: start, End: start + len([]rune("SECRETVALUE"))}

	out, spans := redactMatches(s, []Match{winner, loser})
	count := len(spans)
	if strings.Contains(out, "VALUE") || strings.Contains(out, "SECRET") {
		t.Fatalf("union not masked, leaked bytes: %q", out)
	}
	if out != "prefix [REDACTED] suffix" {
		t.Fatalf("out = %q want the union masked once", out)
	}
	if count != 1 {
		t.Fatalf("count = %d want 1 (one covering interval)", count)
	}
}

// Disjoint matches retain separate placeholders.
func TestRedactMatchesKeepsDisjointSpansSeparate(t *testing.T) {
	s := "aXXXbYYYc"
	one := Match{RuleID: "r", Severity: "high", Start: 1, End: 4} // first run
	two := Match{RuleID: "r", Severity: "high", Start: 5, End: 8} // second run
	out, spans := redactMatches(s, []Match{one, two})
	count := len(spans)
	if out != "a[REDACTED]b[REDACTED]c" {
		t.Fatalf("out = %q", out)
	}
	if count != 2 {
		t.Fatalf("count = %d want 2", count)
	}
}
