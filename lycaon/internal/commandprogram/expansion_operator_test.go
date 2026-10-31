package commandprogram

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

func TestParameterOperatorsFollowShellSemantics(t *testing.T) {
	environment := []string{"FILE=src/pkg/main.go", "EMPTY=", "SPACED=a b", "NAME=hello world", "PATHS=a/b/a/b"}
	cases := []struct {
		name, source string
		want         []string
	}{
		{"default when unset", `: ${UNSET:-fallback} ${UNSET-fallback}`, []string{"fallback", "fallback"}},
		{"colon default covers empty", `: ${EMPTY:-fallback} "${EMPTY-fallback}"`, []string{"fallback", ""}},
		{"default keeps a set value", `: ${FILE:-x}`, []string{"src/pkg/main.go"}},
		{"default word keeps its quoting", `: ${UNSET:-"a b"} ${UNSET:-a b}`, []string{"a b", "a", "b"}},
		{"quoted default stays one field", `: "${UNSET:-a b}"`, []string{"a b"}},
		{"default expands parameters", `: ${UNSET:-$FILE}`, []string{"src/pkg/main.go"}},
		{"alternate when set", `: ${FILE:+set} ${UNSET:+set} ${EMPTY+set} ${EMPTY:+set}`, []string{"set", "set"}},
		{"error operator passes a set value", `: ${FILE:?missing}`, []string{"src/pkg/main.go"}},
		{"shortest and longest prefix", `: ${FILE#*/} ${FILE##*/}`, []string{"pkg/main.go", "main.go"}},
		{"shortest and longest suffix", `: ${FILE%/*} ${FILE%%/*} ${FILE%.go}`, []string{"src/pkg", "src", "src/pkg/main"}},
		{"quoted pattern is literal", `: ${FILE#"*"/}`, []string{"src/pkg/main.go"}},
		{"unmatched pattern keeps value", `: ${FILE%.rs}`, []string{"src/pkg/main.go"}},
		{"replace first and all", `: ${PATHS/a/x} ${PATHS//a/x}`, []string{"x/b/a/b", "x/b/x/b"}},
		{"anchored replacement", `: ${PATHS/#a/x} ${PATHS/%b/y} ${PATHS/#b/z}`, []string{"x/b/a/b", "a/b/a/y", "a/b/a/b"}},
		{"replace with nothing", `: ${PATHS//\//}`, []string{"abab"}},
		{"case conversion", `: ${NAME^} ${NAME^^} ${NAME,,}`, []string{"Hello", "world", "HELLO", "WORLD", "hello", "world"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			memory := testMemory(t)
			scope := testScope(t, memory, environment)
			expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
			got := fieldValues(t, expander, scope, parsedWords(t, c.source)[1:])
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s expanded to %q, want %q", c.source, got, c.want)
			}
			if len(evaluator.calls) != 0 || memory.InUse().Working != 0 {
				t.Fatalf("operator expansion dispatched %v or retained %+v", evaluator.calls, memory.InUse())
			}
			if _, set := scope.Variable("UNSET"); set {
				t.Fatal("an operator assigned state")
			}
		})
	}
}

func TestParameterErrorOperatorRefusesUnsetValue(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, []string{"EMPTY="})
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	for _, source := range []string{`: ${UNSET?set it}`, `: ${EMPTY:?}`} {
		word := parsedWords(t, source)[1]
		_, err := expander.ExpandFields(t.Context(), scope, word)
		var refusal *ExpansionError
		if !errors.As(err, &refusal) || refusal.Code != codeOperandInvalid {
			t.Fatalf("%s: error = %v", source, err)
		}
	}
}

func TestArithmeticExpansionUsesIntegerSemantics(t *testing.T) {
	environment := []string{"N=7", "EMPTY=", "HEX=0x10", "WORD=abc"}
	cases := []struct{ source, want string }{
		{`: $((1 + 2 * 3))`, "7"},
		{`: $(( (1 + 2) * 3 ))`, "9"},
		{`: $((N + 1)) $(($N - 1)) $((${N} * 2))`, "8 6 14"},
		{`: $((UNSET + EMPTY + 1))`, "1"},
		{`: $((-7 / 2)) $((-7 % 2)) $((2 ** 10)) $((1 << 4)) $((HEX))`, "-3 -1 1024 16 16"},
		{`: $((N > 3 && N < 10)) $((N == 7 ? 1 : 2)) $((!N)) $((~0))`, "1 1 0 -1"},
		{`: $((0 && 1 / 0)) $((1 || 1 / 0))`, "0 1"},
		{`: "$((N * 6))"`, "42"},
		{`: $((0xffffffffffffffff)) $((-0xffffffffffffffff)) $((0x8000000000000000))`, "-1 1 -9223372036854775808"},
		{`: $((18446744073709551615)) $((9223372036854775808)) $((-18446744073709551615))`, "-1 -9223372036854775808 1"},
		{`: $((1 << -1)) $((-8 >> -63)) $((3 << 64))`, "-9223372036854775808 -4 3"},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			memory := testMemory(t)
			scope := testScope(t, memory, environment)
			expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
			got := fieldValues(t, expander, scope, parsedWords(t, c.source)[1:])
			if joined := strings.Join(got, " "); joined != c.want {
				t.Fatalf("%s expanded to %q, want %q", c.source, joined, c.want)
			}
		})
	}
	for _, source := range []string{`: $((1 / 0))`, `: $((WORD + 1))`, `: $((2 ** -1))`} {
		memory := testMemory(t)
		expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
		_, err := expander.ExpandFields(t.Context(), testScope(t, memory, environment), parsedWords(t, source)[1])
		var refusal *ExpansionError
		if !errors.As(err, &refusal) || refusal.Code != codeOperandInvalid {
			t.Fatalf("%s: error = %v", source, err)
		}
	}
}
