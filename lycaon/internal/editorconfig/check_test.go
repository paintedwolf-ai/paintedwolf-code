package editorconfig

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func boolPtr(v bool) *bool { return &v }

func strPtr(v string) *string { return &v }

func check(t *testing.T, p Properties, c Change) []Violation {
	t.Helper()
	got, err := Check(p, c)
	if err != nil {
		testutil.FailErr(t, "check", err)
	}
	return got
}

func TestCheckReportsOnlyWrittenLines(t *testing.T) {
	p := Properties{TrimTrailingWhitespace: boolPtr(true), IndentStyle: IndentSpace, IndentSize: 4}
	before := "def f():\n    a = 1 \n\n    return a\n"
	after := "def f():\n    a = 1 \n\t\n    b = 2  \n    return a\n"
	got := check(t, p, Change{Before: &before, After: after})
	want := []Violation{
		{Rule: RuleTrimTrailingWhitespace, Lines: []int{3, 4}, LineCount: 2},
		{Rule: RuleIndentStyle, Lines: []int{3}, LineCount: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Check = %+v, want %+v (line 2 predates the write)", got, want)
	}
}

func TestCheckUndeclaredRulesReportNothing(t *testing.T) {
	after := "x = 1   \n\tpass"
	if got := check(t, Properties{Charset: "utf-8"}, Change{After: after}); got != nil {
		t.Fatalf("undeclared rules should report nothing, got %+v", got)
	}
	if got := check(t, Properties{}, Change{After: after}); got != nil {
		t.Fatalf("no properties should report nothing, got %+v", got)
	}
}

func TestCheckTabStyleAllowsAlignmentAfterTabs(t *testing.T) {
	p := Properties{IndentStyle: IndentTab, TabWidth: 4}
	after := "func f() {\n\tx :=  1\n\t    // aligned\n   * doc\n    y := 2\n}\n"
	got := check(t, p, Change{After: after})
	want := []Violation{{Rule: RuleIndentStyle, Lines: []int{5}, LineCount: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Check = %+v, want %+v", got, want)
	}
}

func TestCheckEndOfLine(t *testing.T) {
	before := "a\r\nb\r\n"
	after := "a\r\nb\nc\r\n"
	got := check(t, Properties{EndOfLine: EndOfLineCRLF}, Change{Before: &before, After: after})
	want := []Violation{{Rule: RuleEndOfLine, Lines: []int{2}, LineCount: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Check crlf = %+v, want %+v", got, want)
	}
	lf := check(t, Properties{EndOfLine: EndOfLineLF, TrimTrailingWhitespace: boolPtr(true)}, Change{After: "a\r\nb\n"})
	if !reflect.DeepEqual(lf, []Violation{{Rule: RuleEndOfLine, Lines: []int{1}, LineCount: 1}}) {
		t.Fatalf("Check lf = %+v; a carriage return is a terminator, not trailing whitespace", lf)
	}
}

func TestCheckFinalNewlineAttributesOnlyIntroducedState(t *testing.T) {
	want := Properties{InsertFinalNewline: boolPtr(true)}
	if got := check(t, want, Change{After: "a\nb"}); !reflect.DeepEqual(got, []Violation{{Rule: RuleInsertFinalNewline, Lines: []int{2}, LineCount: 1}}) {
		t.Fatalf("new file without final newline = %+v", got)
	}
	if got := check(t, want, Change{Before: strPtr("a\nb\n"), After: "a\nc"}); len(got) != 1 {
		t.Fatalf("removing the final newline should be reported, got %+v", got)
	}
	if got := check(t, want, Change{Before: strPtr("a\nb"), After: "A\nb"}); got != nil {
		t.Fatalf("a missing newline the file already lacked is not this write's, got %+v", got)
	}
	strip := Properties{InsertFinalNewline: boolPtr(false)}
	if got := check(t, strip, Change{Before: strPtr(""), After: "a\n"}); len(got) != 1 {
		t.Fatalf("adding a final newline against false should be reported, got %+v", got)
	}
	if got := check(t, want, Change{Before: strPtr("a\n"), After: ""}); got != nil {
		t.Fatalf("an empty file never needs a final newline, got %+v", got)
	}
}

func TestCheckCapsReportedLines(t *testing.T) {
	after := strings.Repeat("x \n", MaxReportedLines+5)
	got := check(t, Properties{TrimTrailingWhitespace: boolPtr(true)}, Change{After: after})
	if len(got) != 1 || len(got[0].Lines) != MaxReportedLines || got[0].LineCount != MaxReportedLines+5 {
		t.Fatalf("Check = %+v", got)
	}
}
