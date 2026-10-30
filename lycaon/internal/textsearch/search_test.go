package textsearch

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func catalogLimits(t *testing.T) Limits {
	t.Helper()
	limits, err := CatalogLimits()
	if err != nil {
		t.Fatalf("catalog limits: %v", err)
	}
	return limits
}

type semanticsCase struct {
	name     string
	dialect  Dialect
	patterns []string
	fold     bool
	word     bool
	line     bool
	input    string
	selected bool
	only     []string // every nonempty match, left to right
}

func (c semanticsCase) spec() Spec {
	return Spec{Patterns: c.patterns, Dialect: c.dialect, IgnoreCase: c.fold, Word: c.word, Line: c.line}
}

func runSemantics(t *testing.T, cases []semanticsCase) {
	t.Helper()
	limits := catalogLimits(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := Compile(t.Context(), c.spec(), limits)
			if err != nil {
				t.Fatalf("compile %q: %v", c.patterns, err)
			}
			_, selected, err := m.Select(t.Context(), []byte(c.input))
			if err != nil || selected != c.selected {
				t.Fatalf("select %q in %q = %v, %v; want %v", c.patterns, c.input, selected, err, c.selected)
			}
			var only []string
			err = m.Each(t.Context(), []byte(c.input), func(s Span) error {
				only = append(only, c.input[s.Start:s.End])
				return nil
			})
			if err != nil || !slices.Equal(only, c.only) {
				t.Fatalf("matches of %q in %q = %q, %v; want %q", c.patterns, c.input, only, err, c.only)
			}
		})
	}
}

func TestBasicRegexSemantics(t *testing.T) {
	runSemantics(t, []semanticsCase{
		{name: "group and back reference", dialect: Basic, patterns: []string{`a\(b\)\1`}, input: "xabb", selected: true, only: []string{"abb"}},
		{name: "back reference must repeat the capture", dialect: Basic, patterns: []string{`\(a\|b\)\1`}, input: "ab ba", selected: false},
		{name: "back reference is case folded under -i", dialect: Basic, patterns: []string{`\(a\)\1`}, fold: true, input: "aA", selected: true, only: []string{"aA"}},
		{name: "interval", dialect: Basic, patterns: []string{`a\{2\}`}, input: "aaa", selected: true, only: []string{"aa"}},
		{name: "open interval", dialect: Basic, patterns: []string{`a\{,2\}b`}, input: "aaab", selected: true, only: []string{"aab"}},
		{name: "ERE operators are literal", dialect: Basic, patterns: []string{`a|b+(c)?`}, input: "a|b+(c)?", selected: true, only: []string{"a|b+(c)?"}},
		{name: "GNU alternation and closures", dialect: Basic, patterns: []string{`x\+\|y\?z`}, input: "xx z yz", selected: true, only: []string{"xx", "z", "yz"}},
		{name: "leading star is literal", dialect: Basic, patterns: []string{`*a`}, input: "b*a", selected: true, only: []string{"*a"}},
		{name: "star after anchor is literal", dialect: Basic, patterns: []string{`^*`}, input: "*x", selected: true, only: []string{"*"}},
		{name: "repeated closure collapses", dialect: Basic, patterns: []string{`ba**`}, input: "baaa", selected: true, only: []string{"baaa"}},
		{name: "dollar in the middle is literal", dialect: Basic, patterns: []string{`x$y`}, input: "x$y", selected: true, only: []string{"x$y"}},
		{name: "caret in the middle is literal", dialect: Basic, patterns: []string{`x^y`}, input: "x^y", selected: true, only: []string{"x^y"}},
		{name: "anchored line", dialect: Basic, patterns: []string{`^ab$`}, input: "ab", selected: true, only: []string{"ab"}},
		{name: "bracket with leading close bracket", dialect: Basic, patterns: []string{`[]a]\+`}, input: "x]a]", selected: true, only: []string{"]a]"}},
		{name: "backslash is a bracket member", dialect: Basic, patterns: []string{`[a\]`}, input: `x\`, selected: true, only: []string{`\`}},
		{name: "negated bracket", dialect: Basic, patterns: []string{`[^]a]`}, input: "a]b", selected: true, only: []string{"b"}},
		{name: "POSIX class", dialect: Basic, patterns: []string{`[[:digit:]]\+`}, input: "ab12c3", selected: true, only: []string{"12", "3"}},
		{name: "GNU word start and end", dialect: Basic, patterns: []string{`\<fo*\>`}, input: "foo xfoo fo", selected: true, only: []string{"foo", "fo"}},
		{name: "GNU word boundary", dialect: Basic, patterns: []string{`\bab`}, input: "cab ab", selected: true, only: []string{"ab"}},
		{name: "GNU word class", dialect: Basic, patterns: []string{`\w\+`}, input: "a_1-b", selected: true, only: []string{"a_1", "b"}},
		{name: "leftmost-longest alternation", dialect: Basic, patterns: []string{`a\|ab`}, input: "ab", selected: true, only: []string{"ab"}},
		{name: "leftmost-longest with a back reference", dialect: Basic, patterns: []string{`\(a\|ab\)\(c\|bcd\)\1*`}, input: "abcd", selected: true, only: []string{"abcd"}},
		{name: "case folding", dialect: Basic, patterns: []string{`hello`}, fold: true, input: "HeLLo", selected: true, only: []string{"HeLLo"}},
		{name: "unicode simple folding", dialect: Basic, patterns: []string{`é`}, fold: true, input: "É", selected: true, only: []string{"É"}},
		{name: "dot is one scalar", dialect: Basic, patterns: []string{`a.b`}, input: "aéb", selected: true, only: []string{"aéb"}},
		{name: "empty pattern selects without output", dialect: Basic, patterns: []string{``}, input: "abc", selected: true},
		{name: "no match", dialect: Basic, patterns: []string{`zz`}, input: "abc", selected: false},
		{name: "collating and equivalence classes", dialect: Basic, patterns: []string{`[[.a.][=b=]]\+`}, input: "xab", selected: true, only: []string{"ab"}},
		{name: "space classes", dialect: Basic, patterns: []string{`\S\s\W`}, input: "a -", selected: true, only: []string{"a -"}},
		{name: "not a word boundary", dialect: Basic, patterns: []string{`a\Bb`}, input: "ab a b", selected: true, only: []string{"ab"}},
		{name: "leading BRE closures are literal", dialect: Basic, patterns: []string{`\+a`}, input: "+a", selected: true, only: []string{"+a"}},
		{name: "escaped punctuation", dialect: Basic, patterns: []string{`a\.b\/`}, input: "a.b/", selected: true, only: []string{"a.b/"}},
		{name: "range with an edge hyphen", dialect: Basic, patterns: []string{`[a-c-]\+`}, input: "x-ab", selected: true, only: []string{"-ab"}},
	})
}

func TestExtendedRegexSemantics(t *testing.T) {
	runSemantics(t, []semanticsCase{
		{name: "POSIX longest overall", dialect: Extended, patterns: []string{`(a|ab)(c|bcd)(d*)`}, input: "abcd", selected: true, only: []string{"abcd"}},
		{name: "alternation prefers longest", dialect: Extended, patterns: []string{`foo|foobar`}, input: "foobar", selected: true, only: []string{"foobar"}},
		{name: "unfinished brace is literal", dialect: Extended, patterns: []string{`a{1`}, input: "a{1", selected: true, only: []string{"a{1"}},
		{name: "interval", dialect: Extended, patterns: []string{`(ab){2}`}, input: "ababab", selected: true, only: []string{"abab"}},
		{name: "GNU back reference", dialect: Extended, patterns: []string{`(ab)\1`}, input: "abab", selected: true, only: []string{"abab"}},
		{name: "closure run collapses", dialect: Extended, patterns: []string{`ba+?`}, input: "b baa", selected: true, only: []string{"b", "baa"}},
		{name: "leading quantifier is literal", dialect: Extended, patterns: []string{`+a`}, input: "+a", selected: true, only: []string{"+a"}},
		{name: "anchors anywhere", dialect: Extended, patterns: []string{`a^b`}, input: "a^b", selected: false},
		{name: "empty alternative", dialect: Extended, patterns: []string{`a|`}, input: "xa", selected: true, only: []string{"a"}},
		{name: "word option retries later matches", dialect: Extended, patterns: []string{`foo`}, word: true, input: "foobar foo", selected: true, only: []string{"foo"}},
		{name: "word option on adjacent words", dialect: Extended, patterns: []string{`foo|bar`}, word: true, input: "foo bar", selected: true, only: []string{"foo", "bar"}},
		{name: "word option shortens a match", dialect: Extended, patterns: []string{`ab*`}, word: true, input: "abb_ ab", selected: true, only: []string{"ab"}},
		{name: "line option", dialect: Extended, patterns: []string{`ab|abc`}, line: true, input: "abc", selected: true, only: []string{"abc"}},
		{name: "line option refuses partial", dialect: Extended, patterns: []string{`ab`}, line: true, input: "abc", selected: false},
		{name: "several patterns renumber references", dialect: Extended, patterns: []string{`(x)\1`, `(y)\1`}, input: "yy xx", selected: true, only: []string{"yy", "xx"}},
		{name: "LF separates patterns", dialect: Extended, patterns: []string{"cat\ndog"}, input: "dog", selected: true, only: []string{"dog"}},
	})
}

func TestPerlRegexSemantics(t *testing.T) {
	runSemantics(t, []semanticsCase{
		{name: "contract word digit case", dialect: Perl, patterns: []string{`\b\w+\s+\d+\b`}, input: "item 42\n", selected: true, only: []string{"item 42"}},
		{name: "contract lookarounds", dialect: Perl, patterns: []string{`(?<=id=)\d+(?=;)`}, input: "id=42;", selected: true, only: []string{"42"}},
		{name: "contract back reference", dialect: Perl, patterns: []string{`(ab)\1`}, input: "abab", selected: true, only: []string{"abab"}},
		{name: "leftmost-first alternation", dialect: Perl, patterns: []string{`a|ab`}, input: "ab", selected: true, only: []string{"a"}},
		{name: "lazy quantifier", dialect: Perl, patterns: []string{`a+?`}, input: "aaa", selected: true, only: []string{"a", "a", "a"}},
		{name: "ASCII digits only", dialect: Perl, patterns: []string{`\d`}, input: "٣", selected: false},
		{name: "ASCII word boundary", dialect: Perl, patterns: []string{`\bé`}, input: "aé", selected: true, only: []string{"é"}},
		{name: "negative lookahead", dialect: Perl, patterns: []string{`foo(?!bar)`}, input: "foobar foobaz", selected: true, only: []string{"foo"}},
		{name: "negative lookbehind", dialect: Perl, patterns: []string{`(?<!x)y`}, input: "xy zy", selected: true, only: []string{"y"}},
		{name: "fixed alternatives of differing width behind", dialect: Perl, patterns: []string{`(?<=ab|c)d`}, input: "abd cd", selected: true, only: []string{"d", "d"}},
		{name: "class escapes inside a class", dialect: Perl, patterns: []string{`[\d_]+`}, input: "a1_2b", selected: true, only: []string{"1_2"}},
		{name: "hex escapes", dialect: Perl, patterns: []string{`\x41\x{42}`}, input: "AB", selected: true, only: []string{"AB"}},
		{name: "POSIX class inside a class", dialect: Perl, patterns: []string{`[[:upper:]]+`}, input: "abCD", selected: true, only: []string{"CD"}},
		{name: "literal brace", dialect: Perl, patterns: []string{`a{x`}, input: "a{x", selected: true, only: []string{"a{x"}},
		{name: "class subtraction is not the dialect", dialect: Perl, patterns: []string{`[a-c-[b]]`}, input: "-]", selected: true, only: []string{"-]"}},
		{name: "case folding", dialect: Perl, patterns: []string{`ABC`}, fold: true, input: "xabc", selected: true, only: []string{"abc"}},
		{name: "word option", dialect: Perl, patterns: []string{`foo`}, word: true, input: "foobar foo", selected: true, only: []string{"foo"}},
		{name: "line option", dialect: Perl, patterns: []string{`a|ab`}, line: true, input: "ab", selected: true, only: []string{"ab"}},
		{name: "several patterns renumber references", dialect: Perl, patterns: []string{`(x)\1`, `(y)\1`}, input: "yy", selected: true, only: []string{"yy"}},
		{name: "zero-width matches advance", dialect: Perl, patterns: []string{`(?=a)`}, input: "aa", selected: true},
		{name: "control escapes", dialect: Perl, patterns: []string{`\t\x7e[\t\x41]`}, input: "\t~A", selected: true, only: []string{"\t~A"}},
		{name: "non-word boundary", dialect: Perl, patterns: []string{`a\Bb`}, input: "ab", selected: true, only: []string{"ab"}},
		{name: "escaped scalar", dialect: Perl, patterns: []string{`\é[\é]`}, input: "éé", selected: true, only: []string{"éé"}},
		{name: "fixed group width behind", dialect: Perl, patterns: []string{`(?<=(?:ab|cd){2})x`}, input: "abcdx", selected: true, only: []string{"x"}},
		{name: "counted quantifiers", dialect: Perl, patterns: []string{`a{2}b{1,}c{0,1}?`}, input: "aabbc", selected: true, only: []string{"aabb"}},
		{name: "literal metacharacters", dialect: Perl, patterns: []string{`a\.b\-c#d e`}, input: "a.b-c#d e", selected: true, only: []string{"a.b-c#d e"}},
	})
}

func TestRE2AndFixedSemantics(t *testing.T) {
	runSemantics(t, []semanticsCase{
		{name: "RE2 leftmost-first", dialect: RE2, patterns: []string{`a|ab`}, input: "ab", selected: true, only: []string{"a"}},
		{name: "RE2 inline flags", dialect: RE2, patterns: []string{`(?i)abc`}, input: "ABC", selected: true, only: []string{"ABC"}},
		{name: "RE2 unicode classes", dialect: RE2, patterns: []string{`\pL+`}, input: "1éa2", selected: true, only: []string{"éa"}},
		{name: "RE2 word option", dialect: RE2, patterns: []string{`foo`}, word: true, input: "foobar foo", selected: true, only: []string{"foo"}},
		{name: "RE2 word option keeps groups", dialect: RE2, patterns: []string{`(f)oo|bar`}, word: true, input: "foo bar", selected: true, only: []string{"foo", "bar"}},
		{name: "RE2 line option", dialect: RE2, patterns: []string{`ab`}, line: true, input: "ab", selected: true, only: []string{"ab"}},
		{name: "fixed metacharacters", dialect: Fixed, patterns: []string{`a.b`}, input: "axb a.b", selected: true, only: []string{"a.b"}},
		{name: "fixed longest at the leftmost offset", dialect: Fixed, patterns: []string{"ab", "abc"}, input: "xabcd", selected: true, only: []string{"abc"}},
		{name: "fixed case folding", dialect: Fixed, patterns: []string{"A.B"}, fold: true, input: "a.b", selected: true, only: []string{"a.b"}},
		{name: "fixed whole line", dialect: Fixed, patterns: []string{"ab"}, line: true, input: "ab", selected: true, only: []string{"ab"}},
		{name: "fixed whole line refuses partial", dialect: Fixed, patterns: []string{"ab"}, line: true, input: "abc", selected: false},
		{name: "fixed word", dialect: Fixed, patterns: []string{"a+"}, word: true, input: "xa+ a+", selected: true, only: []string{"a+"}},
		{name: "fixed exact bytes", dialect: Fixed, patterns: []string{"\xff"}, input: "a\xffb", selected: true, only: []string{"\xff"}},
	})
}

func TestNoPatternsSelectNothing(t *testing.T) {
	m, err := Compile(t.Context(), Spec{Dialect: Basic}, catalogLimits(t))
	testutil.FailErr(t, "compile text-search pattern", err)
	if _, ok, err := m.Select(t.Context(), []byte("anything")); ok || err != nil {
		t.Fatalf("empty pattern set selected: %v %v", ok, err)
	}
}

func TestCaptureGroupsForReplacement(t *testing.T) {
	limits := catalogLimits(t)
	for _, c := range []struct {
		dialect Dialect
		word    bool
		want    [][]int
		groups  int
	}{
		{RE2, false, [][]int{{0, 3, 0, 1}}, 1},
		{RE2, true, [][]int{{0, 3, 0, 1}}, 1},
		{Perl, false, [][]int{{0, 3, 0, 1}}, 1},
		{Extended, false, [][]int{{0, 3}}, 0},
	} {
		m, err := Compile(t.Context(), Spec{Patterns: []string{"(f)oo"}, Dialect: c.dialect, Word: c.word}, limits)
		testutil.FailErr(t, "compile text-search pattern", err)
		var got [][]int
		if err := m.Groups(t.Context(), []byte("foo"), func(g []int) error { got = append(got, slices.Clone(g)); return nil }); err != nil {
			testutil.FailErr(t, "visit capture groups", err)
		}
		if !slices.EqualFunc(got, c.want, slices.Equal) || m.CaptureGroups() != c.groups {
			t.Fatalf("%s word=%v groups %v (%d), want %v (%d)", c.dialect, c.word, got, m.CaptureGroups(), c.want, c.groups)
		}
	}
}
