package commandprogram

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBraceWordsExpandBeforeOtherExpansionsInBashOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"x1.txt", "y1.txt", "y2.txt"} {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}
	cases := []struct {
		source string
		want   []string
	}{
		{`: {a,b}{1,2}`, []string{"a1", "a2", "b1", "b2"}},
		{`: {01..03} {1..10..-3} {10..1..3} {-2..2}`, []string{"01", "02", "03", "1", "4", "7", "10", "10", "7", "4", "1", "-2", "-1", "0", "1", "2"}},
		{`: {a..e} {z..a..12} {A..a..8}`, []string{"a", "b", "c", "d", "e", "z", "n", "b", "A", "I", "Q", "Y", "a"}},
		{`: "{a,b}" '{a,b}' \{a,b\} {a} {1..$N}`, []string{"{a,b}", "{a,b}", "{a,b}", "{a}", "{1..5}"}},
		{`: a{b{c,d},e}f {{a,b},c} {x,{1..2}}`, []string{"abcf", "abdf", "aef", "a", "b", "c", "x", "1", "2"}},
		{`: pre{,.bak} {,} x{,}y`, []string{"pre", "pre.bak", "xy", "xy"}},
		{`: {a,b}"{c,d}" {1..2}"$SPACED"`, []string{"a{c,d}", "b{c,d}", "1a b", "2a b"}},
		{`: ~/{a,b} {~,x}/y`, []string{"/home/u/a", "/home/u/b", "/home/u/y", "x/y"}},
		{`: {$SPACED,x} {x,y}*.txt`, []string{"a", "b", "x", "x1.txt", "y1.txt", "y2.txt"}},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			memory := testMemory(t)
			scope := testScope(t, memory, []string{"HOME=/home/u", "SPACED=a b", "N=5"})
			expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: dir}, memory)
			got := fieldValues(t, expander, scope, parsedWords(t, c.source)[1:])
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s expanded to %q, want %q", c.source, got, c.want)
			}
			if len(evaluator.calls) != 0 || memory.InUse().Working != 0 {
				t.Fatalf("brace expansion dispatched %v or retained %+v", evaluator.calls, memory.InUse())
			}
		})
	}
}

func TestBraceSubstitutionsRunOncePerProducedWord(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, nil)
	expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	got := fieldValues(t, expander, scope, parsedWords(t, `: {a,b}$(echo x)`)[1:])
	if !reflect.DeepEqual(got, []string{"ax", "bx"}) || len(evaluator.calls) != 2 {
		t.Fatalf("fields %q after %d substitutions", got, len(evaluator.calls))
	}
}

func TestBraceFieldsCountAgainstExpandedFields(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, []string{"PAIR=a b"})
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	at, err := expander.ExpandFields(t.Context(), scope, parsedWords(t, `: {1..8192}$PAIR`)[1])
	testutil.FailErr(t, "expand at the field bound", err)
	if len(at) != 16384 || at[16383].Value != "b" {
		t.Fatalf("at-bound fields: %d", len(at))
	}
	_, err = expander.ExpandFields(t.Context(), scope, parsedWords(t, `: {1..8193}$PAIR`)[1])
	var refusal *ExpansionError
	if !errors.As(err, &refusal) || refusal.Limit != "expanded_fields" || refusal.Observed != 16385 {
		t.Fatalf("over-bound refusal: %v", err)
	}
	if memory.InUse().Working != 0 {
		t.Fatalf("refused brace expansion retained %+v", memory.InUse())
	}
}

func TestBraceWordRefusesNamedUserTilde(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, []string{"HOME=/home/u"})
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	word := parsedWords(t, `: {~fixture-user,x}/y`)[1]
	_, err := expander.ExpandFields(t.Context(), scope, word)
	var refusal *ExpansionError
	if !errors.As(err, &refusal) || refusal.Code != codeSyntaxUnsupported || refusal.Span != word.Span {
		t.Fatalf("named-user brace word: %v", err)
	}
}

func hereStringRedirect(t *testing.T, source string) commandsurface.ProgramRedirect {
	t.Helper()
	redirects := parsedStatements(t, source)[0].Redirects
	if len(redirects) != 1 || redirects[0].Operator != "<<<" {
		t.Fatalf("%s redirects: %#v", source, redirects)
	}
	return redirects[0]
}

func TestHereStringExpandsOneValueWithTrailingLF(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "seed glob", os.WriteFile(filepath.Join(dir, "match.txt"), nil, 0o600))
	memory := testMemory(t)
	scope := testScope(t, memory, []string{"HOME=/home/u", "V=a  b"})
	expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: dir}, memory)
	for source, want := range map[string]string{
		`cat <<< abc`:           "abc\n",
		`cat <<< $V`:            "a  b\n",
		`cat <<< "$V"`:          "a  b\n",
		`cat <<< ~/x`:           "/home/u/x\n",
		`cat <<< {a,b}`:         "{a,b}\n",
		`cat <<< *.txt`:         "*.txt\n",
		`cat <<< "$(lines x)"`:  "x\n",
		`cat <<< ''`:            "\n",
		`cat <<< 'q'"$V"\ end`:  "qa  b end\n",
		`cat <<< ${UNSET:-def}`: "def\n",
	} {
		got, err := expander.ExpandDocument(t.Context(), scope, hereStringRedirect(t, source))
		testutil.FailErr(t, "here-string "+source, err)
		if got != want {
			t.Errorf("%s fed %q, want %q", source, got, want)
		}
	}
	if len(evaluator.calls) != 1 || memory.InUse().Working != 0 {
		t.Fatalf("here-strings dispatched %v or retained %+v", evaluator.calls, memory.InUse())
	}
}

func TestHereStringCountsItsLineFeedAgainstHereDocumentBytes(t *testing.T) {
	memory := testMemory(t)
	maximum := expanderLimit(t, "here_document_bytes")
	scope := testScope(t, memory, nil)
	testutil.FailErr(t, "assign at-bound value", scope.Assign(t.Context(), "AT", strings.Repeat("x", int(maximum-1))))
	testutil.FailErr(t, "assign over-bound value", scope.Assign(t.Context(), "OVER", strings.Repeat("x", int(maximum))))
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	at, err := expander.ExpandDocument(t.Context(), scope, hereStringRedirect(t, `cat <<< "$AT"`))
	testutil.FailErr(t, "here-string at the bound", err)
	if int64(len(at)) != maximum || !strings.HasSuffix(at, "x\n") {
		t.Fatalf("at-bound here-string has %d bytes", len(at))
	}
	redirect := hereStringRedirect(t, `cat <<< "$OVER"`)
	_, err = expander.ExpandDocument(t.Context(), scope, redirect)
	var refusal *ExpansionError
	if !errors.As(err, &refusal) || refusal.Limit != "here_document_bytes" || refusal.Observed != maximum+1 || refusal.Span != redirect.Target.Span {
		t.Fatalf("over-bound here-string: %v", err)
	}
}

func expanderLimit(t *testing.T, id string) int64 {
	t.Helper()
	limits, err := catalogEvaluationLimits()
	testutil.FailErr(t, "catalog limits", err)
	return limits[id]
}

func TestAssigningOperatorsSetUnsetOrNullAndExpandToValue(t *testing.T) {
	cases := []struct {
		source   string
		want     []string
		variable string
		value    string
	}{
		{`: ${PORT:=8080}`, []string{"8080"}, "PORT", "8080"},
		{`: ${PORT=8080}`, []string{"8080"}, "PORT", "8080"},
		{`: ${SET:=other}`, []string{"kept"}, "SET", "kept"},
		{`: ${EMPTY=other}`, []string{}, "EMPTY", ""},
		{`: ${EMPTY:=other}`, []string{"other"}, "EMPTY", "other"},
		{`: ${W:=a  b}`, []string{"a", "b"}, "W", "a  b"},
		{`: "${W:=a  b}"`, []string{"a  b"}, "W", "a  b"},
		{`: ${W:="p  q"}`, []string{"p", "q"}, "W", "p  q"},
		{`: "${W:='q'}"`, []string{"'q'"}, "W", "'q'"},
		{`: ${W:=~/h}`, []string{"/home/u/h"}, "W", "/home/u/h"},
		{`: ${W:=$SET-$(echo sub)}`, []string{"kept-sub"}, "W", "kept-sub"},
		{`: ${W:=}`, []string{}, "W", ""},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			memory := testMemory(t)
			scope := testScope(t, memory, []string{"HOME=/home/u", "SET=kept", "EMPTY="})
			expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
			got := fieldValues(t, expander, scope, parsedWords(t, c.source)[1:])
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s expanded to %q, want %q", c.source, got, c.want)
			}
			if value, set := scope.Variable(c.variable); !set || value != c.value {
				t.Fatalf("%s left %s=%q (set %t), want %q", c.source, c.variable, value, set, c.value)
			}
			if memory.InUse().Working != 0 {
				t.Fatalf("assignment retained working memory %+v", memory.InUse())
			}
		})
	}
}

func TestAssigningOperatorSetsOneArrayElement(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, nil)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	got := fieldValues(t, expander, scope, parsedWords(t, `: ${a[2]:=v} ${a[2]:=w} "${a[@]}"`)[1:])
	if !reflect.DeepEqual(got, []string{"v", "v", "v"}) {
		t.Fatalf("array assignment expanded to %q", got)
	}
	if elements, _ := scopeArrayElements(scope, "a"); !reflect.DeepEqual(elements, []string{"v"}) {
		t.Fatalf("array elements %q", elements)
	}
}
