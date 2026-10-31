package commandsurface_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func simpleWords(t *testing.T, p commandsurface.ProgramSnapshot) []commandsurface.DeferredWord {
	t.Helper()
	call, ok := p.Statements[0].Command.(commandsurface.SimpleCommand)
	if !ok {
		t.Fatalf("first statement is %T", p.Statements[0].Command)
	}
	return call.Words
}

func hasBracePart(parts []commandsurface.WordNode) bool {
	for _, part := range parts {
		switch part := part.(type) {
		case commandsurface.BracePart:
			return true
		case commandsurface.DoubleQuotedPart:
			if hasBracePart(part.Parts) {
				return true
			}
		case commandsurface.ParameterPart:
			for _, word := range part.Words() {
				if hasBracePart(word.Parts) {
					return true
				}
			}
		}
	}
	return false
}

func TestBraceWordsLowerToTypedAlternativesAndSequences(t *testing.T) {
	source := "echo {a,b}{1,2} x{01..10..-3}y {z..a..3} pre{,.bak} a{b{c,d},e}f"
	words := simpleWords(t, parseProgram(t, source))
	text := func(word commandsurface.DeferredWord) string {
		return source[word.Span.StartByte:word.Span.EndByte]
	}
	product := words[1].Parts
	if len(product) != 2 {
		t.Fatalf("product parts: %#v", product)
	}
	for _, part := range product {
		brace := part.(commandsurface.BracePart)
		if brace.Sequence != nil || len(brace.Alternatives) != 2 || brace.Span != words[1].Span {
			t.Fatalf("list brace: %#v", brace)
		}
	}
	if first := product[0].(commandsurface.BracePart).Alternatives[0].Parts[0].(commandsurface.LiteralPart); first.Value != "a" {
		t.Fatalf("first alternative: %#v", first)
	}
	padded := words[2].Parts
	sequence := padded[1].(commandsurface.BracePart).Sequence
	if padded[0].(commandsurface.LiteralPart).Value != "x" || padded[2].(commandsurface.LiteralPart).Value != "y" ||
		*sequence != (commandsurface.BraceSequence{Start: 1, End: 10, Step: 3, Width: 2}) {
		t.Fatalf("padded sequence: %#v %#v", padded, sequence)
	}
	letters := words[3].Parts[0].(commandsurface.BracePart).Sequence
	if *letters != (commandsurface.BraceSequence{Start: 'z', End: 'a', Step: 3, Letters: true}) || letters.Count() != 9 {
		t.Fatalf("letter sequence: %#v", letters)
	}
	empty := words[4].Parts[1].(commandsurface.BracePart).Alternatives
	if len(empty) != 2 || len(empty[0].Parts) != 0 || empty[1].Parts[0].(commandsurface.LiteralPart).Value != ".bak" {
		t.Fatalf("empty alternative: %#v", empty)
	}
	nested := words[5].Parts[1].(commandsurface.BracePart).Alternatives[0].Parts
	if len(nested) != 2 || len(nested[1].(commandsurface.BracePart).Alternatives) != 2 {
		t.Fatalf("nested brace: %#v", nested)
	}
	for i, want := range []string{"{a,b}{1,2}", "x{01..10..-3}y", "{z..a..3}", "pre{,.bak}", "a{b{c,d},e}f"} {
		if got := text(words[i+1]); got != want {
			t.Fatalf("word %d spans %q, want %q", i+1, got, want)
		}
	}
}

func TestBraceExpansionAppliesOnlyToFieldWords(t *testing.T) {
	expanding := parseProgram(t, "for i in {1..3}; do a=({a,b} c); done")
	loop := expanding.Statements[0].Command.(commandsurface.ForLoop)
	array := loop.Body[0].Command.(commandsurface.SimpleCommand).Assignments[0].Array
	if !hasBracePart(loop.Items[0].Parts) || !hasBracePart(array[0].Parts) || hasBracePart(array[1].Parts) {
		t.Fatalf("for items or array elements lost brace expansion: %#v %#v", loop.Items, array)
	}
	literal := parseProgram(t, `x={a,b}; cat <<< {a,b}; echo "{a,b}" '{a,b}' \{a,b\} ${v:-{a,b}} ${v/a/{b,c}} {a} {1..$n}; [[ {a,b} == x ]]; case {a,b} in {a,b}) :;; esac`)
	assign := literal.Statements[0].Command.(commandsurface.SimpleCommand).Assignments[0]
	hereString := literal.Statements[1].Redirects[0]
	if hasBracePart(assign.Value.Parts) || hereString.Operator != "<<<" || hereString.HereDocument != nil || hasBracePart(hereString.Target.Parts) {
		t.Fatalf("assignment or here-string expanded braces: %#v %#v", assign, hereString)
	}
	for _, word := range literal.Statements[2].Command.(commandsurface.SimpleCommand).Words {
		if hasBracePart(word.Parts) {
			t.Fatalf("literal brace word expanded: %#v", word)
		}
	}
}

func TestBraceAdmissionRefusesBeforeAnyFieldWithSourceRange(t *testing.T) {
	const prefix = "printf x > ./never; echo "
	cases := []struct {
		word, code, limit string
		observed          int64
	}{
		{"{1..16385}", "COMMAND_LIMIT_EXCEEDED", "expanded_fields", 16385},
		{"{1..128}{1..129}", "COMMAND_LIMIT_EXCEEDED", "expanded_fields", 16385},
		{"{a,b}{1..9223372036854775807}", "COMMAND_LIMIT_EXCEEDED", "expanded_fields", 16385},
		{"{-9223372036854775808..9223372036854775807}", "COMMAND_LIMIT_EXCEEDED", "expanded_fields", 16385},
		{"{a..z..0}", "COMMAND_SYNTAX_UNSUPPORTED", "", 0},
		{"{1..10..0}", "COMMAND_SYNTAX_UNSUPPORTED", "", 0},
		{"{1..a}", "COMMAND_SYNTAX_UNSUPPORTED", "", 0},
		{"x{a,{b..9}}", "COMMAND_SYNTAX_UNSUPPORTED", "", 0},
	}
	for _, c := range cases {
		t.Run(c.word, func(t *testing.T) {
			source := prefix + c.word
			plan, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
			var diagnostic *commandsurface.SyntaxDiagnostic
			if plan != nil || !errors.As(err, &diagnostic) || diagnostic.Code != c.code || diagnostic.Limit != c.limit || diagnostic.Observed != c.observed {
				t.Fatalf("diagnostic %+v err=%v", diagnostic, err)
			}
			if got := source[diagnostic.Span.StartByte:diagnostic.Span.EndByte]; got != c.word || diagnostic.Span.Line != 1 || diagnostic.Span.Column != len(prefix)+1 {
				t.Fatalf("diagnostic spans %q at %+v, want %q", got, diagnostic.Span, c.word)
			}
			if c.limit != "" && diagnostic.Maximum != 16384 {
				t.Fatalf("bound %+v", diagnostic)
			}
		})
	}
	for _, source := range []string{"echo {1..16384}", "echo {1..128}{1..128}", "echo {-9223372036854775808..9223372036854775807..2251799813685248}"} {
		parseProgram(t, source)
	}
}

func TestBraceRefusesOutsideFieldWordsWhereBashWouldNotExpandSafely(t *testing.T) {
	for _, source := range []string{"export X={a,b}", "cat > ./out{a,b}", "cat 2>&{1,2}", "trap 'echo' {EXIT,INT}"} {
		plan, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
		var diagnostic *commandsurface.SyntaxDiagnostic
		if plan != nil || !errors.As(err, &diagnostic) || diagnostic.Code != "COMMAND_SYNTAX_UNSUPPORTED" {
			t.Fatalf("%s: plan=%v err=%v", source, plan, err)
		}
	}
}

func TestBraceSequenceCountAndValuesSpanTheIntegerRange(t *testing.T) {
	cases := []struct {
		sequence commandsurface.BraceSequence
		count    uint64
		last     int64
	}{
		{commandsurface.BraceSequence{Start: 1, End: 10, Step: 3}, 4, 10},
		{commandsurface.BraceSequence{Start: 10, End: 1, Step: 3}, 4, 1},
		{commandsurface.BraceSequence{Start: 5, End: 5, Step: 1}, 1, 5},
		{commandsurface.BraceSequence{Start: math.MaxInt64 - 2, End: math.MaxInt64, Step: 1}, 3, math.MaxInt64},
		{commandsurface.BraceSequence{Start: math.MinInt64 + 1, End: math.MinInt64, Step: 7}, 1, math.MinInt64 + 1},
		{commandsurface.BraceSequence{Start: math.MinInt64, End: math.MaxInt64, Step: 1 << 63}, 2, 0},
		{commandsurface.BraceSequence{Start: math.MinInt64, End: math.MaxInt64, Step: 1}, math.MaxUint64, math.MaxInt64 - 1},
	}
	for _, c := range cases {
		if got := c.sequence.Count(); got != c.count {
			t.Fatalf("%+v count = %d, want %d", c.sequence, got, c.count)
		}
		if got := c.sequence.Value(c.count - 1); got != c.last {
			t.Fatalf("%+v last = %d, want %d", c.sequence, got, c.last)
		}
	}
}

func TestUntilHereStringAndAssigningOperatorsLower(t *testing.T) {
	p := parseProgram(t, "until test -f ./ready; do :; done; while false; do :; done; wc -c <<< \"$x\"; : ${PORT:=8080} ${HOST=localhost} ${a[1]:=v} ${IFS:=,}")
	until := p.Statements[0].Command.(commandsurface.WhileLoop)
	while := p.Statements[1].Command.(commandsurface.WhileLoop)
	if !until.Until || while.Until || len(until.Condition) != 1 || len(until.Body) != 1 {
		t.Fatalf("loop kinds: %#v %#v", until, while)
	}
	hereString := p.Statements[2].Redirects[0]
	if hereString.Operator != "<<<" || hereString.HereDocument != nil || len(hereString.Target.Parts) != 1 {
		t.Fatalf("here-string: %#v", hereString)
	}
	words := p.Statements[3].Command.(commandsurface.SimpleCommand).Words
	for i, want := range []string{":=", "=", ":=", ":="} {
		part := words[i+1].Parts[0].(commandsurface.ParameterPart)
		if part.Operator != want || part.Argument == nil {
			t.Fatalf("operator %d: %#v", i, part)
		}
	}
	for _, source := range []string{"echo ${1:=x}", "echo ${@:=x}", "echo ${?:=x}", "echo ${PIPESTATUS:=x}", "echo ${a[@]:=x}"} {
		plan, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
		var diagnostic *commandsurface.SyntaxDiagnostic
		if plan != nil || !errors.As(err, &diagnostic) || diagnostic.Code != "COMMAND_SYNTAX_UNSUPPORTED" || source[diagnostic.Span.StartByte:diagnostic.Span.EndByte] != strings.TrimPrefix(source, "echo ") {
			t.Fatalf("%s: diagnostic %+v err=%v", source, diagnostic, err)
		}
	}
}

func TestBraceSnapshotsAreDetached(t *testing.T) {
	source := "echo {a,$(printf b)}{1..3}"
	p, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "fixture.pw"})
	testutil.FailErr(t, "parse brace program", err)
	baseline := p.Snapshot()
	mutated := p.Snapshot()
	word := simpleWords(t, mutated)[1]
	list := word.Parts[0].(commandsurface.BracePart)
	list.Alternatives[0].Parts[0] = commandsurface.LiteralPart{Value: "changed"}
	word.Parts[1].(commandsurface.BracePart).Sequence.End = 99
	if !reflect.DeepEqual(p.Snapshot(), baseline) {
		t.Fatal("snapshot mutation changed the frozen brace plan")
	}
	if !reflect.DeepEqual(parseProgram(t, source), parseProgram(t, source)) {
		t.Fatal("stable brace source generated different node identities")
	}
}
