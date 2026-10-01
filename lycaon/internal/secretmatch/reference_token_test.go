package secretmatch

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	tokenTestID      = "88f10188-886e-4215-a6ef-0889c7ab4d54"
	tokenTestOtherID = "ed9354ed-712e-45c3-a074-67b3776764d7"
	// keycloakClientSecret is shaped for kingfisher.keycloak.2.
	keycloakClientSecret = "7b3e1234-abcd-4567-8901-23456789abcd"
)

// A secret_generate result and a command's env name the vendor next to the
// token, which carries both "secret:" and a UUID.
func TestDetectorsNeverMatchReferenceTokens(t *testing.T) {
	matcher, err := BuildMatcher(Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	if hits := matcher.Screen("keycloak client secret: " + keycloakClientSecret); !hasRule(hits, "kingfisher.keycloak.2") {
		t.Fatalf("control: the Keycloak rule did not match a bare client secret: %#v", hits)
	}
	token, other := ReferenceToken(tokenTestID), ReferenceToken(tokenTestOtherID)
	for name, text := range map[string]string{
		"generated result": `{"created":true,"secret":{"reference":"` + token + `","name":"todos-keycloak-client-secret"}}`,
		"command env":      `{"KEYCLOAK_ADMIN_PASSWORD":"` + other + `","KEYCLOAK_CLIENT_SECRET":"` + token + `"}`,
		"assignment":       "keycloak.credentials.secret=" + token,
		"adjacent tokens":  "keycloak secret: " + token + other,
	} {
		if hits := matcher.Screen(text); len(hits) != 0 {
			t.Errorf("%s: a reference token produced findings: %#v", name, hits)
		}
		if redacted := matcher.RedactString(context.Background(), text); redacted != text {
			t.Errorf("%s: redaction rewrote a reference token: %q", name, redacted)
		}
	}
}

// A rule keyed on a password field must not skip over a masked token and
// capture the key that follows it.
func TestMaskedTokenIsNotSkippedToTheNextField(t *testing.T) {
	matcher, err := BuildMatcher(Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	text := `{"POSTGRES_PASSWORD":"` + ReferenceToken(tokenTestID) + `","SESSION_SECRET_ROTATION_WINDOW":"86400"}` +
		"\npassword = " + ReferenceToken(tokenTestOtherID) + " session_cookie_signing_name"
	if hits := matcher.Screen(text); len(hits) != 0 {
		t.Fatalf("a field after a masked token was reported: %#v", hits)
	}
}

// Masking a token must not hide a real value beside it.
func TestTokenMaskKeepsNeighboringFindings(t *testing.T) {
	matcher, err := BuildMatcher(Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	text := "reference " + ReferenceToken(tokenTestID) + "\nkeycloak client secret: " + keycloakClientSecret
	hits := matcher.Screen(text)
	if !hasRule(hits, "kingfisher.keycloak.2") {
		t.Fatalf("the real client secret beside a token was not found: %#v", hits)
	}
	redacted := matcher.RedactString(context.Background(), text)
	if strings.Contains(redacted, keycloakClientSecret) || !strings.Contains(redacted, ReferenceToken(tokenTestID)) {
		t.Fatalf("redaction = %q, want the value removed and the token intact", redacted)
	}
}

// A value remembered as evidence that equals a token's id never matches inside
// the token, though it still matches where it stands alone.
func TestRememberedValueEqualToTokenIDMatchesOnlyOutsideTokens(t *testing.T) {
	m := matcherWithHarvest(t, HarvestedValue{
		Name: "KEYCLOAK_CLIENT_SECRET", Secret: tokenTestID, RuleID: HarvestRuleID,
		Fingerprint: SecretFingerprint("sf1_remembered"),
	})
	if hits := m.ScreenContext(context.Background(), `"env":"`+ReferenceToken(tokenTestID)+`"`); len(hits) != 0 {
		t.Fatalf("remembered id matched inside its token: %#v", hits)
	}
	if hits := m.ScreenContext(context.Background(), "id "+tokenTestID); len(hits) != 1 {
		t.Fatalf("remembered id standing alone = %#v, want one match", hits)
	}
}

func TestMaskOutsideClipsHitsAtTokenBytes(t *testing.T) {
	text := "ab" + ReferenceToken(tokenTestID) + "cd"
	mask := maskReferenceTokens(text)
	if len(mask.text) != len(text) || strings.Contains(mask.text, "paintedwolf") {
		t.Fatalf("mask changed length or kept token bytes: %q", mask.text)
	}
	tokenEnd := 2 + len(ReferenceToken(tokenTestID))
	got := mask.outside([]Match{
		{RuleID: "straddle", Start: 0, End: len(text)},
		{RuleID: "inside", Start: 3, End: 10},
		{RuleID: "clear", Start: tokenEnd, End: tokenEnd + 2},
	})
	want := []Match{
		{RuleID: "straddle", Start: 0, End: 2},
		{RuleID: "straddle", Start: tokenEnd, End: tokenEnd + 2},
		{RuleID: "clear", Start: tokenEnd, End: tokenEnd + 2},
	}
	if len(got) != len(want) {
		t.Fatalf("outside = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i].RuleID != want[i].RuleID || got[i].Start != want[i].Start || got[i].End != want[i].End {
			t.Fatalf("outside[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMalformedReferenceTokenGrammar(t *testing.T) {
	token := ReferenceToken(tokenTestID)
	for text, want := range map[string]bool{
		"":                                    false,
		"plain text":                          false,
		token:                                 false,
		token + token:                         false,
		"{{paintedwolf-secret:[REDACTED]}}":   true,
		"{{paintedwolf-secret:" + tokenTestID: true,
		token + " {{paintedwolf-secret:":      true,
		"{{paintedwolf-secret:" + strings.ToUpper(tokenTestID) + "}}": true,
	} {
		if got := ContainsMalformedReferenceToken(text); got != want {
			t.Errorf("ContainsMalformedReferenceToken(%q) = %v, want %v", text, got, want)
		}
	}
	if id, ok := ParseReferenceToken(" " + token + "\n"); !ok || id != tokenTestID {
		t.Fatalf("ParseReferenceToken = %q, %v", id, ok)
	}
}

func TestWithinReferenceTokens(t *testing.T) {
	line := `KC_CLIENT_SECRET: "` + ReferenceToken(tokenTestID) + `"`
	if !WithinReferenceTokens(line, tokenTestID) {
		t.Fatal("a scanner finding on a token id was kept")
	}
	if WithinReferenceTokens(line+" "+tokenTestID, tokenTestID) {
		t.Fatal("a value that also stands alone on the line was dropped")
	}
	if WithinReferenceTokens("secret: "+tokenTestID, tokenTestID) {
		t.Fatal("a line without tokens reported token coverage")
	}
}
