package secretmatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEncodedFormsSpellsEverySerializedForm(t *testing.T) {
	const value = `pa"ss\word/+ &?#~`
	forms := EncodedForms(value)
	quoted, err := json.Marshal(value)
	testutil.FailErr(t, "json.Marshal", err)
	escaped := strings.Trim(string(quoted), `"`)
	requoted, err := json.Marshal(escaped)
	testutil.FailErr(t, "json.Marshal", err)
	for name, want := range map[string]string{
		"query":        url.QueryEscape(value),
		"path":         url.PathEscape(value),
		"json":         escaped,
		"json in json": strings.Trim(string(requoted), `"`),
		"base64":       base64.StdEncoding.EncodeToString([]byte(value)),
		"base64 url":   base64.RawURLEncoding.EncodeToString([]byte(value)),
	} {
		found := false
		for _, form := range forms {
			found = found || form == want
		}
		if !found {
			t.Errorf("%s spelling %q is missing from %q", name, want, forms)
		}
	}
	for i, form := range forms {
		if form == value {
			t.Fatal("the plain value is listed as its own encoded form")
		}
		for _, other := range forms[:i] {
			if other == form {
				t.Fatalf("form %q is listed twice", form)
			}
		}
	}
}

// A value no serializer rewrites has only its base64 spellings, so the screen
// does little extra work for it.
func TestEncodedFormsOfAnUnchangedValueAreOnlyBase64(t *testing.T) {
	const value = "plainvalue1234"
	forms := EncodedForms(value)
	// Standard and URL alphabets agree on this value, so only padding differs.
	if len(forms) != 2 {
		t.Fatalf("forms = %q, want the padded and unpadded base64 spellings", forms)
	}
	for _, form := range forms {
		if !strings.HasPrefix(form, "cGxhaW52YWx1ZTEyMzQ") {
			t.Fatalf("form %q is not a base64 spelling of the value", form)
		}
	}
	if EncodedForms("") != nil {
		t.Fatal("an empty value has spellings")
	}
}

func managedEvidence(secret, reference string) HarvestedValue {
	return HarvestedValue{
		Name: "deploy token", Container: "managed secret", Secret: secret,
		RuleID: ManagedRuleID, Title: ManagedRuleTitle, Source: SourceRememberedMatch,
		Reference: reference, NonDisclosable: true,
	}
}

// A tool result carries file content as a JSON string, so the bytes the model
// would read are the escaped spelling, and the reference must replace those.
func TestManagedValueIsRecognizedInItsSerializedSpellings(t *testing.T) {
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"
	for name, secret := range map[string]string{
		"backslash":        `Az7\Kp9!Tr2zz`,
		"double quote":     `pa"ss-word-9911`,
		"gcp key fragment": `"private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg"`,
		"url reserved":     `to&ken=with?reserved/chars`,
	} {
		t.Run(name, func(t *testing.T) {
			m := matcherWithHarvest(t, managedEvidence(secret, reference))
			serialized, err := json.Marshal(map[string]any{"content": "1\tDB_PASSWORD=" + secret + "\n2\tPORT=3000"})
			testutil.FailErr(t, "json.Marshal", err)
			out, replacements := m.ProjectLabeledWhere(context.Background(), "", string(serialized), nil)
			quoted, _ := json.Marshal(secret)
			if strings.Contains(out, strings.Trim(string(quoted), `"`)) {
				t.Fatalf("the escaped spelling survived: %s", out)
			}
			if len(replacements) != 1 || replacements[0].Reference != reference {
				t.Fatalf("replacements = %+v, want one reference", replacements)
			}
			var decoded map[string]string
			testutil.FailErr(t, "decode projected JSON", json.Unmarshal([]byte(out), &decoded))
			if decoded["content"] != "1\tDB_PASSWORD="+reference+"\n2\tPORT=3000" {
				t.Fatalf("content = %q, want the reference in place of the value", decoded["content"])
			}
			percent := "https://example.test/callback?token=" + url.QueryEscape(secret)
			redacted, _ := m.ProjectLabeledWhere(context.Background(), "", percent, nil)
			if strings.Contains(redacted, url.QueryEscape(secret)) || !strings.Contains(redacted, reference) {
				t.Fatalf("percent-encoded spelling was not written as the reference: %s", redacted)
			}
		})
	}
}

// One line of a multi-line value is evidence but not the value its reference
// names, in any spelling.
func TestALineOfAMultiLineValueCarriesNoReferenceInAnySpelling(t *testing.T) {
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"
	const secret = "-----BEGIN KEY-----\nabc/def+ghi==\n-----END KEY-----"
	m := matcherWithHarvest(t, managedEvidence(secret, reference))
	line := url.QueryEscape("abc/def+ghi==")
	out, replacements := m.ProjectLabeledWhere(context.Background(), "", "fragment "+line+" seen", nil)
	if strings.Contains(out, line) {
		t.Fatalf("the encoded line survived: %s", out)
	}
	if len(replacements) != 1 || replacements[0].Reference != "" {
		t.Fatalf("replacements = %+v, want a placeholder without a reference", replacements)
	}
	hits := m.ScreenContext(context.Background(), "whole "+url.QueryEscape(secret))
	if len(hits) != 1 || hits[0].Reference != reference {
		t.Fatalf("hits = %+v, want the whole encoded value to carry its reference", hits)
	}
}

// A base64 spelling inside a longer encoded token is not a standalone
// occurrence of a harvested value.
func TestBase64SpellingOfAHarvestedValueStaysWordBounded(t *testing.T) {
	const secret = "q4Wv8ZbN2mKx7Lp3"
	encoded := base64.StdEncoding.EncodeToString([]byte(secret))
	m := harvestMatcher(t, HarvestedValue{Name: "TOKEN", Container: ".env", Secret: secret, Fingerprint: "sf1_harvest"})
	if hits := m.ScreenContext(context.Background(), "blob AAAA"+encoded+"BBBB end"); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none inside a longer token", hits)
	}
	if hits := m.ScreenContext(context.Background(), "value "+encoded+" end"); len(hits) != 1 {
		t.Fatalf("hits = %+v, want the standalone base64 spelling", hits)
	}
}
