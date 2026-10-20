package commandsurface_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func parseProgram(t *testing.T, source string) commandsurface.ProgramSnapshot {
	t.Helper()
	p, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "fixture.pw"})
	testutil.FailErr(t, "parse program", err)
	return p.Snapshot()
}

func TestVersionOneGrammarIsLoweredWithoutExecution(t *testing.T) {
	cases := []string{
		`printf '%s\n' 'quoted | ; && $HOME' "double $name" escaped\ word`,
		"printf 'one\ntwo\n' | grep two && true || false; :",
		"{ cd ./fixture; pwd; }; ( export VALUE=one; unset VALUE )",
		"VALUE=one; VALUE=two env -i KEY=value printenv KEY",
		"if test -f ./input; then cat ./input; elif [ -e ./other ]; then :; else false; fi",
		"for item in one two; do echo \"$item\"; done",
		"for item; do break; done",
		"while false; do continue; done",
		"helper() { printf '%s' \"$1\"; return 0; }; helper ok",
		"[[ -n \"$name\" && ( \"$name\" == a* || ! -f ./missing ) ]]",
		"cat < ./input > ./output 2>> ./errors; cat 2>&1; cat 1>&2",
		"cat &> ./all; cat &>> ./all; cat >> ./output",
		"cat <<'END'\n$(literal) $name\nEND\n",
		"cat <<-END\n\t$name\n\tEND\n",
		"result=$(printf ok); back=`printf yes`; printf '%s' \"${#result} $0 $1 ${10} $# $? $! $@ $*\"",
		"sleep 1 & wait \"$!\"; jobs; source -- ./script.pw",
		"echo ./src/**/*.go; ! false",
	}
	for _, source := range cases {
		t.Run(source, func(t *testing.T) {
			p := parseProgram(t, source)
			if p.Version != 1 || len(p.Statements) == 0 || p.SourceDigest == "" || p.CatalogDigest == "" {
				t.Fatalf("incomplete plan: %#v", p)
			}
		})
	}
}

func TestQuotedMetacharactersAndOriginalSourceSpans(t *testing.T) {
	source := "  printf '%s' 'α|\nβ'\n"
	p := parseProgram(t, source)
	s := p.Statements[0]
	if s.Span.StartByte != 2 || s.Span.Line != 1 || s.Span.Column != 3 {
		t.Fatalf("statement span: %#v", s.Span)
	}
	call := s.Command.(commandsurface.SimpleCommand)
	word := call.Words[2]
	if string([]byte(source)[word.Span.StartByte:word.Span.EndByte]) != "'α|\nβ'" {
		t.Fatalf("word span: %#v", word.Span)
	}
	part := word.Parts[0].(commandsurface.SingleQuotedPart)
	if part.Value != "α|\nβ" || len(p.Statements) != 1 {
		t.Fatalf("quoted data changed: %#v", p)
	}
}

func TestWholeUnitRefusesMalformedOrExcludedLaterSyntax(t *testing.T) {
	cases := []string{
		"printf changed > ./output; if false; then echo 'unterminated; fi",
		"printf changed > ./output; if false; then eval dangerous; fi",
		"echo <(cat ./input)", "echo $((1+2))", "((1+2))", "a=(one two)",
		"echo ${name:-fallback}", "echo ${name/x/y}", "echo ${!name}", "echo $-", "echo $$",
		"echo {one,two}", "case x in x) true;; esac", "until false; do :; done",
		"select x in one; do :; done", "coproc echo x", "trap true EXIT", "set -e",
		"cat <<< value", "cat 3> ./output", "cat 2>&3", "cat 1<> ./input",
		"function helper { :; }", "cat() { :; }", "local x=value", "IFS=, echo x",
		"echo $'escaped'", "echo $\"localized\"", "[[ -S ./sock ]]", "for ((i=0;i<1;i++)); do :; done",
	}
	for _, source := range cases {
		t.Run(source, func(t *testing.T) {
			p, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
			var diagnostic *commandsurface.SyntaxDiagnostic
			if p != nil || !errors.As(err, &diagnostic) {
				t.Fatalf("program=%#v error=%v", p, err)
			}
			if diagnostic.Code == "" || diagnostic.Span.Line < 1 {
				t.Fatalf("missing typed diagnosis: %#v", diagnostic)
			}
		})
	}
}

func TestSnapshotsAndDeferredSubstitutionsAreDetached(t *testing.T) {
	source := []byte("if false; then echo $(printf invisible); else echo visible; fi")
	p, err := commandsurface.ParseProgram(source, commandsurface.ParseOptions{SourceName: "fixture.pw"})
	testutil.FailErr(t, "parse deferred branches", err)
	baseline := p.Snapshot()
	first := p.Snapshot()
	branch := first.Statements[0].Command.(commandsurface.Conditional)
	call := branch.Then[0].Command.(commandsurface.SimpleCommand)
	sub := call.Words[1].Parts[0].(commandsurface.SubstitutionPart)
	sub.Statements[0].Background = true
	branch.Then[0].Background = true
	first.Statements[0].Command = commandsurface.SimpleCommand{}
	source[0] = 'x'
	if !reflect.DeepEqual(p.Snapshot(), baseline) {
		t.Fatal("caller mutation changed frozen syntax")
	}
	if !reflect.DeepEqual(parseProgram(t, string([]byte("if false; then echo $(printf invisible); else echo visible; fi"))), baseline) {
		t.Fatal("stable source generated different node identities")
	}
}

func TestEncodingVersionsAndAdmissionCeilings(t *testing.T) {
	if _, err := commandsurface.ParseProgram([]byte(":"), commandsurface.ParseOptions{CatalogDigest: "different-catalog"}); err == nil {
		t.Fatal("mislabeled frozen catalog")
	}

	for _, source := range [][]byte{{0xff}, {'e', 'c', 'h', 'o', 0}, {0xef, 0xbb, 0xbf, '#'}} {
		if p, err := commandsurface.ParseProgram(source, commandsurface.ParseOptions{}); p != nil || err == nil {
			t.Fatalf("accepted invalid bytes: %x", source)
		}
	}
	if _, err := commandsurface.ParseProgram([]byte(":"), commandsurface.ParseOptions{Version: 2}); err == nil {
		t.Fatal("accepted unknown version")
	}
	for _, source := range []string{strings.Repeat(" ", 65537), strings.Repeat("( ", 100) + ":" + strings.Repeat(" )", 100), "printf " + strings.Repeat("a ", 8500)} {
		_, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
		var diagnostic *commandsurface.SyntaxDiagnostic
		if !errors.As(err, &diagnostic) || diagnostic.Code != "COMMAND_LIMIT_EXCEEDED" {
			t.Fatalf("expected ceiling, got %v", err)
		}
	}
	source := []byte("#" + strings.Repeat("x", 65536))
	if _, err := commandsurface.ParseProgram(source, commandsurface.ParseOptions{Sourced: true}); err != nil {
		t.Fatalf("source ceiling confused with submitted ceiling: %v", err)
	}
}

func TestSourceUnitFailureDoesNotReclassifyParent(t *testing.T) {
	parent := parseProgram(t, "printf retained > ./parent; source -- ./script.pw")
	_, err := commandsurface.ParseProgram([]byte("printf forbidden > ./child; if"), commandsurface.ParseOptions{Sourced: true, SourceName: "script.pw"})
	if err == nil || len(parent.Statements) != 2 {
		t.Fatalf("source refusal changed parent plan: %v %#v", err, parent)
	}
	// Effects are settled by their existing owners. A separate source-unit refusal
	// supplies no fact that could claim zero earlier parent effects or permit replay.
}

func FuzzParseProgramNeverExecutes(f *testing.F) {
	for _, source := range []string{":", "echo 'a|b'", "if false; then echo $(cat secret); fi", "cat <<END\n$NAME\nEND", "echo \"$@\""} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		p, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
		if err != nil {
			return
		}
		one, two := p.Snapshot(), p.Snapshot()
		if !reflect.DeepEqual(one, two) {
			t.Fatal("non-deterministic snapshot")
		}
		for _, statement := range one.Statements {
			if statement.Span.StartByte < 0 || statement.Span.EndByte > len(source) || statement.Span.StartByte > statement.Span.EndByte {
				t.Fatalf("out-of-source span: %#v", statement.Span)
			}
		}
	})
}

func TestEncodingDiagnosticsAddressOriginalOffendingBytes(t *testing.T) {
	for _, source := range [][]byte{append([]byte("#α\n  "), 0), append([]byte("#α\n  "), 0xff)} {
		_, err := commandsurface.ParseProgram(source, commandsurface.ParseOptions{})
		var diagnostic *commandsurface.SyntaxDiagnostic
		if !errors.As(err, &diagnostic) || diagnostic.Span.StartByte != len(source)-1 || diagnostic.Span.EndByte != len(source) || diagnostic.Span.Line != 2 || diagnostic.Span.Column != 3 {
			t.Fatalf("encoding span=%#v err=%v", diagnostic, err)
		}
	}
}
func TestHereDocumentExpansionAndRedirectOrderStayStructured(t *testing.T) {
	p := parseProgram(t, "cat 2>&1 > ./out <<'END'\n${literal} {a,b}\nEND\n")
	redirects := p.Statements[0].Redirects
	if len(redirects) != 3 || redirects[0].Operator != ">&" || redirects[0].Descriptor != 2 || redirects[1].Operator != ">" || redirects[2].HereDocument == nil {
		t.Fatalf("redirect order changed: %#v", redirects)
	}
	parts := redirects[2].HereDocument.Parts
	if len(parts) != 1 || parts[0].(commandsurface.LiteralPart).Value != "${literal} {a,b}\n" {
		t.Fatalf("quoted here-document executed expansions: %#v", parts)
	}
	for _, source := range []string{"echo ~fixture-user/path", "VALUE=~fixture-user/path"} {
		if _, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{}); err == nil {
			t.Fatalf("named-user lookup admitted: %s", source)
		}
	}
	parseProgram(t, "echo '~fixture-user/path' \\~fixture-user/path")
}

func TestTestExpressionBracesAreDeferredPatternAndRegexData(t *testing.T) {
	for _, source := range []string{`[[ aaa =~ a{1,3} ]]`, `[[ '{a,b}' == {a,b} ]]`, `[[ aaa =~ a{2} && "$name" != {x,y} ]]`} {
		parseProgram(t, source)
	}
	for _, source := range []string{`echo {a,b}`, `[[ aaa =~ $(echo {a,b}) ]]`, `[[ ~fixtureUser == x ]]`} {
		if plan, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{}); plan != nil || err == nil {
			t.Fatalf("unsupported expansion admitted: %s", source)
		}
	}
}

func TestForeignDialectRefusalPreservesItsActualLaterSpan(t *testing.T) {
	source := `printf ok; echo ${(q)name}`
	plan, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
	var diagnostic *commandsurface.SyntaxDiagnostic
	if plan != nil || !errors.As(err, &diagnostic) || diagnostic.Code != "COMMAND_SYNTAX_UNSUPPORTED" || diagnostic.Span.StartByte < strings.Index(source, "${") || diagnostic.Span.Line != 1 || diagnostic.Span.Column != diagnostic.Span.StartByte+1 {
		t.Fatalf("foreign dialect diagnostic lost offending span: %+v %v", diagnostic, err)
	}
}

func TestLiteralExcludedCommandsUseShellQuoteRemovalBeforeAdmission(t *testing.T) {
	for _, name := range []string{`e\val`, `ev"al"`, `'eval'`, `tr\ap`, `s\et`} {
		source := "printf changed > ./output; " + name + " data"
		plan, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
		var diagnostic *commandsurface.SyntaxDiagnostic
		if plan != nil || !errors.As(err, &diagnostic) || diagnostic.Code != "COMMAND_SYNTAX_UNSUPPORTED" || diagnostic.Span.StartByte != strings.Index(source, name) {
			t.Fatalf("excluded literal bypassed complete-unit admission: %q %+v %v", name, diagnostic, err)
		}
	}
	parseProgram(t, `printf '%s' 'e\val' "ev\al"`)
}
