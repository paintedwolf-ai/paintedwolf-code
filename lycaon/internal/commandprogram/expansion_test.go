package commandprogram

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

const secretToken = "{{paintedwolf-secret:0f8fad5b-d9cb-469f-a165-70867728950e}}"

func TestExpandFieldsQuotingSplittingAndParameters(t *testing.T) {
	environment := []string{
		"HOME=/home/u", "SPACED=/h o", "EMPTY=", "LEAD= b", "PAD= a b ",
		"MIXED=a  b\tc\nd", "COLON=a:b c", "UNICODE=héllo", "INJECT=; rm -rf / && echo $(touch x) `id` > out",
		"SECRET=" + secretToken,
	}
	letters := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	cases := []struct {
		name, source string
		positional   []string
		want         []string
	}{
		{"escaped space", `: a\ b`, nil, []string{"a b"}},
		{"quotes keep one field", `: "x y" 'p q' ""`, nil, []string{"x y", "p q", ""}},
		{"double-quote escapes", `: "\$MIXED\"\\\n" '$MIXED\n'`, nil, []string{`$MIXED"\\n`, `$MIXED\n`}},
		{"fixed whitespace splitting", `: $MIXED`, nil, []string{"a", "b", "c", "d"}},
		{"quoted expansion is one field", `: "$MIXED"`, nil, []string{"a  b\tc\nd"}},
		{"unset IFS uses default whitespace", `: $COLON`, nil, []string{"a:b", "c"}},
		{"surrounding separators split", `: x$PAD`, nil, []string{"x", "a", "b"}},
		{"leading separator after empty quotes", `: ""$LEAD`, nil, []string{"", "b"}},
		{"empty unquoted vanishes", `: $EMPTY $UNSET`, nil, []string{}},
		{"empty quoted stays", `: "$EMPTY" $EMPTY""`, nil, []string{"", ""}},
		{"quoted at-sign with none", `: "$@"`, nil, []string{}},
		{"quoted at-sign with empty quotes", `: "$@"""`, nil, []string{""}},
		{"quoted at-sign keeps operands", `: "$@"`, []string{"a b", ""}, []string{"a b", ""}},
		{"quoted at-sign joins neighbours", `: "x$@y"`, []string{"1", "2"}, []string{"x1", "2y"}},
		{"unquoted at-sign splits", `: $@ $*`, []string{"a b", "c"}, []string{"a", "b", "c", "a", "b", "c"}},
		{"quoted star joins", `: "$*"`, []string{"a", "b c"}, []string{"a b c"}},
		{"special parameters", `: $# $? $! $0 $1 ${10} $10 $11`, letters, []string{"10", "3", "%1", "inline", "a", "j", "a0", "a1"}},
		{"lengths", `: ${#UNICODE} "${#@}" ${#UNSET}`, letters, []string{"5", "10", "0"}},
		{"tilde", `: ~ ~/x "~" \~/x x~`, nil, []string{"/home/u", "/home/u/x", "~", "~/x", "x~"}},
		{"metacharacters stay data", `: $INJECT`, nil, []string{";", "rm", "-rf", "/", "&&", "echo", "$(touch", "x)", "`id`", ">", "out"}},
		{"secret reference stays a reference", `: "$SECRET" pre$SECRET`, nil, []string{secretToken, "pre" + secretToken}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			memory := testMemory(t)
			scope := testScope(t, memory, environment, c.positional...)
			scope.SetStatus(3)
			scope.SetBackground("%1")
			expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
			got := fieldValues(t, expander, scope, parsedWords(t, c.source)[1:])
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s expanded to %q, want %q", c.source, got, c.want)
			}
			if len(evaluator.calls) != 0 || scope.LastStatus() != 3 {
				t.Fatalf("expansion dispatched %v or changed status to %d", evaluator.calls, scope.LastStatus())
			}
			if memory.InUse().Working != 0 {
				t.Fatalf("expansion retained working memory %+v", memory.InUse())
			}
		})
	}
}

func TestTildeWithoutHomeStaysLiteral(t *testing.T) {
	memory := testMemory(t)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	got := fieldValues(t, expander, testScope(t, memory, nil), parsedWords(t, `: ~/x`)[1:])
	if !reflect.DeepEqual(got, []string{"~/x"}) {
		t.Fatalf("tilde without HOME = %q", got)
	}
}

func TestExpandScalarAndPatternKeepOneValue(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, []string{"V=a  b", "STAR=*"}, "p", "q")
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	for source, want := range map[string]string{
		`x=$V`: "a  b", `x="$@"`: "p q", `x=$@`: "p q", `x=$STAR`: "*", `x=~`: "~", `x=`: "", `x=$(lines o)`: "o",
	} {
		got, err := expander.ExpandScalar(t.Context(), scope, parsedAssignment(t, source).Value)
		testutil.FailErr(t, "scalar "+source, err)
		if got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
	statement := parsedStatements(t, `[[ x == a"*"$STAR\? ]]`)[0]
	test := statement.Command.(commandsurface.TestExpression).Expression.(commandsurface.BinaryPredicate)
	got, err := expander.ExpandPattern(t.Context(), scope, test.Right.(commandsurface.TestWord).Word)
	testutil.FailErr(t, "pattern", err)
	want := PredicateValue{{Value: "a"}, {Value: "*", Quoted: true}, {Value: "*"}, {Value: "?", Quoted: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pattern fragments %+v, want %+v", got, want)
	}
}

func TestTestExpressionMatchesThroughRealExpansion(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, []string{"NAME=a*z", "GLOB=a*"})
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	runtime := PredicateRuntime{Expansion: expander, Scope: scope, Memory: memory, Patterns: NativePredicatePatterns{Memory: memory}}
	for source, status := range map[string]int{
		`[[ $NAME == $GLOB ]]`: 0, `[[ $NAME == "$GLOB" ]]`: 1, `[[ "a*" == "$GLOB" ]]`: 0, `[[ abz == $GLOB ]]`: 0, `[[ abz == "$GLOB" ]]`: 1,
	} {
		expression := parsedStatements(t, source)[0].Command.(commandsurface.TestExpression)
		result, err := runtime.TestExpression(t.Context(), fixtureInvocation(), expression)
		testutil.FailErr(t, source, err)
		if result.Status != status {
			t.Errorf("%s status %d, want %d", source, result.Status, status)
		}
	}
}

func TestExpandDocumentBodies(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, []string{"V=\tval", "Q=\"q\""})
	expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	cases := []struct{ source, want string }{
		{"cat <<E\na $V \\$V \"$Q\" \\\" $(echo s) `echo b`\n\\\\x\nE\n", "a \tval $V \"\"q\"\" \\\" s b\n\\x\n"},
		{"cat <<'E'\nlit $V \\$V $(echo s)\nE\n", "lit $V \\$V $(echo s)\n"},
		{"cat <<\"E\"\nlit $V\nE\n", "lit $V\n"},
		{"cat <<\\E\nlit $V\nE\n", "lit $V\n"},
		{"cat <<-E\n\t\tindented $V\n\tE\n", "indented \tval\n"},
		{"cat <<E\nE\n", ""},
	}
	for _, c := range cases {
		redirect := parsedStatements(t, c.source)[0].Redirects[0]
		got, err := expander.ExpandDocument(t.Context(), scope, redirect)
		testutil.FailErr(t, "document "+c.source, err)
		if got != c.want {
			t.Errorf("%q expanded to %q, want %q", c.source, got, c.want)
		}
	}
	if len(evaluator.calls) != 2 {
		t.Fatalf("quoted bodies ran substitutions: %+v", evaluator.calls)
	}
	if _, err := expander.ExpandDocument(t.Context(), scope, parsedStatements(t, "cat < f")[0].Redirects[0]); err == nil {
		t.Fatal("expanded a file redirect as a here-document")
	}
}

func globFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, file := range []string{"a.go", "b.go", "Z.go", ".hidden.go", "-rf", "notes.txt", "sub/c.go", "sub/deep/d.go", "sub/.cache/e.go", "space dir/g.go"} {
		path := filepath.Join(root, filepath.FromSlash(file))
		testutil.FailErr(t, "mkdir "+file, os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write "+file, os.WriteFile(path, nil, 0o644))
	}
	return root
}

func TestExpandFieldsGlobsSortedOverRealTree(t *testing.T) {
	root := globFixture(t)
	environment := []string{"P=*.go", "D=space dir", "Q=[ab].go"}
	cases := []struct {
		source string
		want   []string
	}{
		{`: *.go`, []string{"Z.go", "a.go", "b.go"}},
		{`: .*.go`, []string{".hidden.go"}},
		{`: "."*.go`, []string{".hidden.go"}},
		{`: "*".go \*.go '*.go'`, []string{"*.go", "*.go", "*.go"}},
		{`: *.none`, []string{"*.none"}},
		{`: $P "$P"`, []string{"Z.go", "a.go", "b.go", "*.go"}},
		{`: $Q`, []string{"a.go", "b.go"}},
		{`: **/*.go`, []string{"Z.go", "a.go", "b.go", "space dir/g.go", "sub/c.go", "sub/deep/d.go"}},
		{`: sub/**`, []string{"sub/c.go", "sub/deep", "sub/deep/d.go"}},
		{`: */`, []string{"space dir/", "sub/"}},
		{`: $D/*.go "$D"/*.go`, []string{"space", "dir/*.go", "space dir/g.go"}},
		{`: -*`, []string{"-rf"}},
		{`: ./n*`, []string{"./notes.txt"}},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			memory := testMemory(t)
			expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: root}, memory)
			got := fieldValues(t, expander, testScope(t, memory, environment), parsedWords(t, c.source)[1:])
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s expanded to %q, want %q", c.source, got, c.want)
			}
		})
	}
	memory := testMemory(t)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: root}, memory)
	scope := testScope(t, memory, nil)
	got, err := expander.ExpandScalar(t.Context(), scope, parsedAssignment(t, `x=*.go`).Value)
	testutil.FailErr(t, "scalar glob", err)
	pattern, err := expander.ExpandPattern(t.Context(), scope, parsedWords(t, `: *.go`)[1])
	testutil.FailErr(t, "pattern glob", err)
	if got != "*.go" || len(pattern) != 1 || pattern[0].Value != "*.go" || strings.Contains(got, "a.go") {
		t.Fatalf("scalar/pattern expansion globbed: %q %+v", got, pattern)
	}
}
