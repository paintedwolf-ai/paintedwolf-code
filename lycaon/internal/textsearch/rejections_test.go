package textsearch

import (
	"errors"
	"strings"
	"testing"
)

func TestPatternRejections(t *testing.T) {
	limits := catalogLimits(t)
	for _, c := range []struct {
		dialect Dialect
		pattern string
		code    string
	}{
		{Basic, `\(a`, CodeRegexInvalid},
		{Basic, `a\)`, CodeRegexInvalid},
		{Basic, `a\{1`, CodeRegexInvalid},
		{Basic, `a\{2,1\}`, CodeRegexInvalid},
		{Basic, `\1\(a\)`, CodeRegexInvalid},
		{Basic, `\(a\1\)`, CodeRegexInvalid},
		{Basic, `[a`, CodeRegexInvalid},
		{Basic, `[[:nope:]]`, CodeRegexInvalid},
		{Basic, `a\`, CodeRegexInvalid},
		{Basic, `\d`, CodeRegexUnsupported},
		{Basic, `a\{1001\}`, CodeRegexUnsupported},
		{Basic, "\U0010FF01", CodeRegexUnsupported},
		{Extended, `(a`, CodeRegexInvalid},
		{Extended, `a)`, CodeRegexInvalid},
		{Extended, `a{3,2}`, CodeRegexInvalid},
		{Extended, `\n`, CodeRegexUnsupported},
		{Perl, `(?R)`, CodeRegexUnsupported},
		{Perl, `(?<=a+)b`, CodeRegexUnsupported},
		{Perl, `[`, CodeRegexInvalid},
		{Perl, `(?<name>a)`, CodeRegexUnsupported},
		{Perl, `(?P<name>a)`, CodeRegexUnsupported},
		{Perl, `(?i)a`, CodeRegexUnsupported},
		{Perl, `(?>a)`, CodeRegexUnsupported},
		{Perl, `(?(1)a|b)`, CodeRegexUnsupported},
		{Perl, `(?C1)`, CodeRegexUnsupported},
		{Perl, `(*FAIL)`, CodeRegexUnsupported},
		{Perl, `a*+`, CodeRegexUnsupported},
		{Perl, `\p{L}`, CodeRegexUnsupported},
		{Perl, `\R`, CodeRegexUnsupported},
		{Perl, `\1(a)`, CodeRegexUnsupported},
		{Perl, `(a\1)`, CodeRegexUnsupported},
		{Perl, `\2(a)`, CodeRegexInvalid},
		{Perl, `a{2,1}`, CodeRegexInvalid},
		{Perl, `a{,2}`, CodeRegexUnsupported},
		{Perl, `a**`, CodeRegexInvalid},
		{Perl, `*a`, CodeRegexInvalid},
		{Perl, `(?<=a(?:b|cd))x`, CodeRegexUnsupported},
		{Perl, `(?=a)*`, CodeRegexUnsupported},
		{Perl, `a)`, CodeRegexInvalid},
		{Basic, `a\{1,x\}`, CodeRegexInvalid},
		{Basic, `a\{40000\}`, CodeRegexInvalid},
		{Basic, `a\{2\}*`, CodeRegexUnsupported},
		{Basic, `a*\{2\}`, CodeRegexUnsupported},
		{Basic, `a\}`, CodeRegexInvalid},
		{Basic, `\{1\}`, CodeRegexInvalid},
		{Basic, "[\U0010FF01]", CodeRegexUnsupported},
		{Basic, `[[:alpha:]`, CodeRegexInvalid},
		{Basic, `[[:alpha`, CodeRegexInvalid},
		{Extended, "\\\U0010FF01", CodeRegexUnsupported},
		{Extended, `a{2}{3}`, CodeRegexUnsupported},
		{Perl, `\x{110000}`, CodeRegexInvalid},
		{Perl, `\x{41`, CodeRegexInvalid},
		{Perl, `\xZ`, CodeRegexInvalid},
		{Perl, `[\D]`, CodeRegexUnsupported},
		{Perl, `[\q]`, CodeRegexUnsupported},
		{Perl, `[[:nope:]]`, CodeRegexInvalid},
		{Perl, `[a-\d]`, CodeRegexInvalid},
		{Perl, `[a-[:digit:]]`, CodeRegexInvalid},
		{Perl, `[a`, CodeRegexInvalid},
		{Perl, `[a\`, CodeRegexInvalid},
		{Perl, `(a)\12`, CodeRegexUnsupported},
		{Perl, `(a)(?<=\1)`, CodeRegexUnsupported},
		{Perl, `(?#comment)`, CodeRegexUnsupported},
		{Perl, `(a`, CodeRegexInvalid},
		{Perl, "a\xff", CodeRegexInvalid},
		{Perl, "[\xff]", CodeRegexInvalid},
		{Perl, `a\`, CodeRegexInvalid},
		{Perl, `a{1001}`, CodeRegexUnsupported},
		{Perl, `a?+`, CodeRegexUnsupported},
		{Perl, `a{2}{3}`, CodeRegexInvalid},
		{Perl, `{2}`, CodeRegexInvalid},
		{Fixed, "\xff", ""},
		{RE2, `a(b`, CodeRegexInvalid},
		{RE2, `(a)\1`, CodeRegexInvalid},
		{RE2, `(?<=a)b`, CodeRegexInvalid},
	} {
		t.Run(string(c.dialect)+" "+c.pattern, func(t *testing.T) {
			if c.code == "" {
				// Fixed strings are exact bytes until -i or -w makes them a pattern.
				if _, err := Compile(t.Context(), Spec{Patterns: []string{c.pattern}, Dialect: c.dialect}, limits); err != nil {
					t.Fatalf("exact bytes: %v", err)
				}
				_, err := Compile(t.Context(), Spec{Patterns: []string{c.pattern}, Dialect: c.dialect, IgnoreCase: true}, limits)
				var pattern *PatternError
				if !errors.As(err, &pattern) || pattern.Code != CodeRegexUnsupported || !strings.Contains(err.Error(), CodeRegexUnsupported) {
					t.Fatalf("folded invalid UTF-8: %v", err)
				}
				return
			}
			_, err := Compile(t.Context(), Spec{Patterns: []string{c.pattern}, Dialect: c.dialect}, limits)
			var pattern *PatternError
			if !errors.As(err, &pattern) || pattern.Code != c.code {
				t.Fatalf("compile %q: %v, want %s", c.pattern, err, c.code)
			}
		})
	}
}
