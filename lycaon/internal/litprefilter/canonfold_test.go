package litprefilter

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// The class invariant: every rune a case-insensitive matcher of either family
// can equate with r folds to the same value as r.
func TestCanonicalFoldIsInvariantUnderEveryCaseRelation(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		want := CanonicalFold(r)
		for _, related := range [...]rune{unicode.SimpleFold(r), unicode.ToLower(r), unicode.ToUpper(r), unicode.ToTitle(r)} {
			if got := CanonicalFold(related); got != want {
				t.Fatalf("CanonicalFold(%U) = %U but CanonicalFold(%U) = %U", related, got, r, want)
			}
		}
	}
}

func TestCanonicalFoldJoinsOrbitsAndSpecialLowercase(t *testing.T) {
	cases := map[string][]rune{
		"ascii letters share uppercase": {'k', 'K'},
		"kelvin sign joins k":           {'k', 'K'},
		"long s joins s":                {'s', 'S', 'ſ'},
		"dotted and dotless I join i":   {'i', 'I', 'İ', 'ı'},
		"ohm joins omega":               {'ω', 'Ω', 'Ω'},
		"sharp s pair":                  {'ß', 'ẞ'},
	}
	for name, class := range cases {
		want := CanonicalFold(class[0])
		for _, r := range class[1:] {
			if got := CanonicalFold(r); got != want {
				t.Fatalf("%s: CanonicalFold(%U) = %U, want %U", name, r, got, want)
			}
		}
	}
	if CanonicalFold('7') != '7' || CanonicalFold('中') != '中' {
		t.Fatal("caseless runes must fold to themselves")
	}
}

func TestAppendCanonicalFoldMatchesRuneFold(t *testing.T) {
	in := "Straße İstanbul Kelvin naïve\xffraw"
	got := string(AppendCanonicalFold(nil, []byte(in)))
	var want strings.Builder
	for i := 0; i < len(in); {
		r, size := utf8.DecodeRuneInString(in[i:])
		if r == utf8.RuneError && size == 1 {
			want.WriteByte(in[i])
		} else {
			want.WriteRune(CanonicalFold(r))
		}
		i += size
	}
	if got != want.String() {
		t.Fatalf("AppendCanonicalFold = %q, want %q", got, want.String())
	}
}
