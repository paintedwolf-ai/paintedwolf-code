package briefing

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestContractKeepsWholeSentencesWithinBounds(t *testing.T) {
	cases := []struct {
		name     string
		contract Contract
		raw      string
		want     string
	}{
		{"first of several", OneSentence, "Rebuild the bundle. Then rerun tests.", "Rebuild the bundle."},
		{"two allowed", TwoSentences, "Ran the tests. 12 passed. Coverage rose.", "Ran the tests. 12 passed."},
		{"code spans keep periods", OneSentence, "Updates `config.yaml` and `pkg.Run()`. Next it waits.", "Updates `config.yaml` and `pkg.Run()`."},
		{"lowercase continuation is one sentence", OneSentence, "Uses defaults, e.g. retries. Then exits.", "Uses defaults, e.g. retries."},
		{"decimals stay", OneSentence, "Bumps the version to 1.5 for release.", "Bumps the version to 1.5 for release."},
		{"markup removed", OneSentence, "- **Clear** the [token](https://x) cache.", "Clear the token cache."},
		{"quotes trimmed", OneSentence, `"Regenerate the fixtures."`, "Regenerate the fixtures."},
		{"unterminated keeps text", OneSentence, "Moves the token check ahead of the redirect", "Moves the token check ahead of the redirect"},
		{"unbalanced backtick dropped", OneSentence, "Edits the `main function.", "Edits the main function."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.contract.Parse(c.raw)
			testutil.FailErr(t, "parse", err)
			if got != c.want {
				t.Fatalf("got %q; want %q", got, c.want)
			}
		})
	}
}

func TestContractNeverExceedsItsRuneBound(t *testing.T) {
	long := "Word " + strings.Repeat("word ", 200) + "end."
	for _, contract := range []Contract{OneSentence, TwoSentences} {
		got, err := contract.Parse(long)
		testutil.FailErr(t, "parse long", err)
		if utf8.RuneCountInString(got) > contract.MaxRunes {
			t.Fatalf("%d runes exceed %d", utf8.RuneCountInString(got), contract.MaxRunes)
		}
	}
	got, err := TwoSentences.Parse("Short one. " + long)
	testutil.FailErr(t, "parse mixed", err)
	if got != "Short one." {
		t.Fatalf("a second sentence that does not fit was kept: %q", got)
	}
	multibyte := strings.Repeat("é", 400)
	got, _ = OneSentence.Parse(multibyte)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) > OneSentence.MaxRunes {
		t.Fatalf("multibyte clamp broke UTF-8 or bound: %d", utf8.RuneCountInString(got))
	}
}

func TestContractRefusesOutputWithoutText(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n- \n", `""`, "**  **", "  **  ", "…", "---"} {
		if _, err := OneSentence.Parse(raw); !errors.Is(err, ErrNoText) {
			t.Fatalf("%q parsed as success: %v", raw, err)
		}
	}
	if _, err := (Contract{}).Parse("text"); err == nil {
		t.Fatal("unbounded contract accepted")
	}
}

func TestLabelledContractKeepsEveryBlockInOrder(t *testing.T) {
	contract := Contract{MaxRunes: 600, Labels: []string{"Contribution", "Verification", "Remaining"}}
	got, err := contract.Parse("**Verification:** Ran go test.\n- Contribution: Added token refresh\nacross two files.\nRemaining: Nothing.")
	testutil.FailErr(t, "parse", err)
	want := "Contribution: Added token refresh across two files.\nVerification: Ran go test.\nRemaining: Nothing."
	if got != want {
		t.Fatalf("got %q", got)
	}
	if _, err := contract.Parse("Contribution: Added it.\nVerification: Ran tests."); !errors.Is(err, ErrNoText) {
		t.Fatalf("a missing block parsed: %v", err)
	}
	long, err := contract.Parse("Contribution: " + strings.Repeat("word ", 300) + "\nVerification: ok\nRemaining: none")
	testutil.FailErr(t, "parse long", err)
	if utf8.RuneCountInString(long) > contract.MaxRunes {
		t.Fatalf("%d runes exceed the bound", utf8.RuneCountInString(long))
	}
}
