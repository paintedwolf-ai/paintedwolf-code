package textsearch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
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
		{RE2, `a(b`, CodeRegexInvalid},
		{RE2, `(a)\1`, CodeRegexInvalid},
		{RE2, `(?<=a)b`, CodeRegexInvalid},
	} {
		t.Run(string(c.dialect)+" "+c.pattern, func(t *testing.T) {
			_, err := Compile(t.Context(), Spec{Patterns: []string{c.pattern}, Dialect: c.dialect}, limits)
			var pattern *PatternError
			if !errors.As(err, &pattern) || pattern.Code != c.code {
				t.Fatalf("compile %q: %v, want %s", c.pattern, err, c.code)
			}
		})
	}
}

func budgetResource(t *testing.T, err error) string {
	t.Helper()
	var budget *BudgetError
	if !errors.As(err, &budget) {
		t.Fatalf("want budget error, got %v", err)
	}
	return budget.Resource
}

// TestRegexBudgets exercises each engine's resource ceilings: the linear
// engine at its exact catalog step boundary, the bounded engines on
// catastrophic backtracking, and pattern and record size.
func TestRegexBudgets(t *testing.T) {
	limits := catalogLimits(t)
	ctx := t.Context()
	linear, err := Compile(ctx, Spec{Patterns: []string{`b`}, Dialect: Extended}, limits)
	if err != nil {
		t.Fatal(err)
	}
	at := []byte(strings.Repeat("a", int(limits.WorkSteps)-1))
	if _, ok, err := linear.Select(ctx, at); err != nil || ok {
		t.Fatalf("record at the step ceiling: %v %v", ok, err)
	}
	over := append(at, 'a')
	if _, _, err := linear.Select(ctx, over); budgetResource(t, err) != "regex_work_steps" {
		t.Fatalf("record over the step ceiling: %v", err)
	}

	catastrophic := []byte(strings.Repeat("a", 40))
	for _, spec := range []Spec{
		{Patterns: []string{`(a*)*b`}, Dialect: Perl},
		{Patterns: []string{`\(a*\)*\1b`}, Dialect: Basic},
		{Patterns: []string{`(a|aa)*c`}, Dialect: Extended, Word: true},
	} {
		m, err := Compile(ctx, spec, limits)
		if err != nil {
			t.Fatalf("%v: %v", spec.Patterns, err)
		}
		// A loaded host must not reach the time ceiling first.
		vm := m.engine.(vmEngine)
		vm.limits.CPU = time.Minute
		m.engine = vm
		if _, _, err := m.Select(ctx, catastrophic); budgetResource(t, err) != "regex_work_steps" {
			t.Fatalf("%v select: %v", spec.Patterns, err)
		}
	}

	long := strings.Repeat("a", int(limits.PatternBytes)+1)
	for _, dialect := range []Dialect{Basic, Extended, Perl, RE2} {
		if _, err := Compile(ctx, Spec{Patterns: []string{long}, Dialect: dialect}, limits); budgetResource(t, err) != "regex_pattern_bytes" {
			t.Fatalf("%s pattern bytes: %v", dialect, err)
		}
	}
	if _, err := Compile(ctx, Spec{Patterns: []string{long}, Dialect: Fixed}, limits); err != nil {
		t.Fatalf("fixed strings are not regex patterns: %v", err)
	}

	small := limits
	small.InputBytes = 4
	for _, dialect := range []Dialect{Fixed, Basic, Perl} {
		m, err := Compile(ctx, Spec{Patterns: []string{"a"}, Dialect: dialect}, small)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := m.Select(ctx, []byte("aaaaa")); budgetResource(t, err) != "regex_input_bytes" {
			t.Fatalf("%s record bytes: %v", dialect, err)
		}
	}

	m, err := Compile(ctx, Spec{Patterns: []string{`(a|b)*c`}, Dialect: Perl}, limits)
	if err != nil {
		t.Fatal(err)
	}
	vm := m.engine.(vmEngine)
	vm.limits.CPU = time.Nanosecond
	m.engine = vm
	if _, _, err := m.Select(ctx, []byte(strings.Repeat("ab", 2000))); budgetResource(t, err) != "regex_cpu_ms" {
		t.Fatalf("cpu ceiling: %v", err)
	}
}

// TestBoundedSessionStepBoundary is the catalog's injected work-step pair:
// exactly regex_work_steps is admitted and one more step stops the record.
func TestBoundedSessionStepBoundary(t *testing.T) {
	limits := catalogLimits(t)
	m, err := Compile(t.Context(), Spec{Patterns: []string{`(?=a)`}, Dialect: Perl}, limits)
	if err != nil {
		t.Fatal(err)
	}
	program := m.engine.(vmEngine).program
	session, err := program.Session(t.Context(), limits.syntaxBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Charge(limits.WorkSteps); err != nil {
		t.Fatalf("at ceiling: %v", err)
	}
	if err := session.Charge(1); budgetResource(t, budgetError(err)) != "regex_work_steps" {
		t.Fatalf("over ceiling: %v", err)
	}
}

func TestCompileHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Compile(ctx, Spec{Patterns: []string{"a"}, Dialect: Basic}, catalogLimits(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled compile: %v", err)
	}
}

func TestNoPatternsSelectNothing(t *testing.T) {
	m, err := Compile(t.Context(), Spec{Dialect: Basic}, catalogLimits(t))
	if err != nil {
		t.Fatal(err)
	}
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
		if err != nil {
			t.Fatal(err)
		}
		var got [][]int
		if err := m.Groups(t.Context(), []byte("foo"), func(g []int) error { got = append(got, slices.Clone(g)); return nil }); err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(got, c.want, slices.Equal) || m.CaptureGroups() != c.groups {
			t.Fatalf("%s word=%v groups %v (%d), want %v (%d)", c.dialect, c.word, got, m.CaptureGroups(), c.want, c.groups)
		}
	}
}
