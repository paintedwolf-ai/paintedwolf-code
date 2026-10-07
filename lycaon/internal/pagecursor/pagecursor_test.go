package pagecursor

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type listPosition struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

var testPages = For[listPosition]("test_list")

func TestCodecRoundTripsTypedPositionAsURLSafeToken(t *testing.T) {
	want := listPosition{CreatedAt: "2026-09-27T10:00:00Z", ID: "a/b?c=d&e"}
	token, err := testPages.Encode(Scope("project-a"), want)
	testutil.FailErr(t, "encode cursor", err)
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(token) {
		t.Fatalf("token %q is not URL-safe", token)
	}
	if strings.Contains(token, want.ID) {
		t.Fatalf("token %q exposes the position in clear text", token)
	}
	got, err := testPages.Decode(token, Scope("project-a"))
	testutil.FailErr(t, "decode cursor", err)
	if got != want {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
}

func TestCodecRejectsCursorFromAnotherKindOrScope(t *testing.T) {
	token, err := testPages.Encode(Scope("project-a", "open"), listPosition{ID: "x"})
	testutil.FailErr(t, "encode cursor", err)
	other := For[listPosition]("other_list")
	cases := map[string]func() error{
		"other project": func() error { _, err := testPages.Decode(token, Scope("project-b", "open")); return err },
		"other filter":  func() error { _, err := testPages.Decode(token, Scope("project-a", "closed")); return err },
		"other kind":    func() error { _, err := other.Decode(token, Scope("project-a", "open")); return err },
	}
	for name, decode := range cases {
		if err := decode(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
}

func TestScopePartsDoNotAlias(t *testing.T) {
	if Scope("ab", "c") == Scope("a", "bc") || Scope("a\x00b") == Scope("a", "b") || Scope("") == Scope() {
		t.Fatal("distinct scope part lists produced the same binding")
	}
}

func TestCodecRejectsTamperedAndMalformedTokens(t *testing.T) {
	scope := Scope("project-a")
	token, err := testPages.Encode(scope, listPosition{ID: "x"})
	testutil.FailErr(t, "encode cursor", err)
	payload, signature, _ := strings.Cut(token, ".")
	flip := func(s string) string {
		if s[0] == 'A' || s[0] == '0' {
			return "1" + s[1:]
		}
		return "0" + s[1:]
	}
	flipInside := func(s string) string {
		if len(s) > 4 {
			idx := len(s) - 2
			replacement := "B"
			if s[idx] == 'B' {
				replacement = "A"
			}
			return s[:idx] + replacement + s[idx+1:]
		}
		return flip(s)
	}
	for name, candidate := range map[string]string{
		"empty":               "",
		"frame byte edited":   flip(payload) + "." + signature,
		"payload body edited": flipInside(payload) + "." + signature,
		"signature edited":    payload + "." + flip(signature),
		"signature missing":   payload,
		"extra segment":       token + ".x",
		"not base64":          "!!!." + signature,
		"oversized":           strings.Repeat("A", maxEncodedBytes+1),
	} {
		if _, err := testPages.Decode(candidate, scope); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
}

func TestCodecRejectsPositionWithUnknownFields(t *testing.T) {
	loose := For[map[string]any]("test_list")
	token, err := loose.Encode(Scope("p"), map[string]any{"id": "x", "extra": true})
	testutil.FailErr(t, "encode cursor", err)
	if _, err := testPages.Decode(token, Scope("p")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestGenerationCursorExpiresWhenGenerationIsNotRetained(t *testing.T) {
	offsets := For[int]("test_offsets")
	scope := Scope("repo")
	token, err := offsets.EncodeAt(scope, 7, 40)
	testutil.FailErr(t, "encode cursor", err)

	generation, offset, err := offsets.DecodeAt(token, scope, Current(7))
	testutil.FailErr(t, "decode retained generation", err)
	if generation != 7 || offset != 40 {
		t.Fatalf("decoded generation %d offset %d", generation, offset)
	}
	if _, _, err := offsets.DecodeAt(token, scope, Current(8)); !errors.Is(err, ErrExpired) {
		t.Fatalf("superseded generation error = %v, want ErrExpired", err)
	}
	if _, err := offsets.EncodeAt(scope, 0, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unpublished generation error = %v, want ErrInvalid", err)
	}
}

func TestGenerationAndPlainCursorsDoNotCross(t *testing.T) {
	offsets := For[int]("test_offsets")
	scope := Scope("repo")
	plain, err := offsets.Encode(scope, 3)
	testutil.FailErr(t, "encode plain cursor", err)
	generational, err := offsets.EncodeAt(scope, 2, 3)
	testutil.FailErr(t, "encode generation cursor", err)
	if _, _, err := offsets.DecodeAt(plain, scope, Current(2)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plain cursor through DecodeAt: %v", err)
	}
	if _, err := offsets.Decode(generational, scope); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generation cursor through Decode: %v", err)
	}
}

func TestCursorFromPreviousEngineProcessIsExpired(t *testing.T) {
	scope := Scope("project-a")
	token, err := testPages.Encode(scope, listPosition{ID: "x"})
	testutil.FailErr(t, "encode cursor", err)

	savedKey, savedInstance := tokenKey, processInstance
	tokenKey, processInstance = randomBytes(32), "0123456789abcdef"
	t.Cleanup(func() { tokenKey, processInstance = savedKey, savedInstance })

	if _, err := testPages.Decode(token, scope); !errors.Is(err, ErrExpired) {
		t.Fatalf("previous-process cursor error = %v, want ErrExpired", err)
	}
	if _, err := testPages.Decode(token, Scope("project-b")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("previous-process cursor for another scope error = %v, want ErrInvalid", err)
	}
}

func TestSealedTokenNeverStartsWithJSONObjectBase64(t *testing.T) {
	token, err := testPages.Encode(Scope("project-a"), listPosition{ID: "x"})
	testutil.FailErr(t, "encode cursor", err)
	if strings.HasPrefix(token, "eyJ") {
		t.Fatalf("token %q starts with base64 JSON object prefix eyJ", token)
	}
	if !strings.HasPrefix(token, "AX") {
		t.Fatalf("token %q does not start with framed prefix AX", token)
	}
}

func TestOpenRejectsUnframedToken(t *testing.T) {
	scope := Scope("project-a")
	raw, err := json.Marshal(envelope{
		Kind: "test_list", Scope: scopeDigest(scope), Process: processInstance,
		Value: json.RawMessage(`{"created_at":"","id":"x"}`),
	})
	testutil.FailErr(t, "marshal raw envelope", err)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	token := payload + "." + base64.RawURLEncoding.EncodeToString(sign(payload))
	if _, err := testPages.Decode(token, scope); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unframed token error = %v, want ErrInvalid", err)
	}
}

