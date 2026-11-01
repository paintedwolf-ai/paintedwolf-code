package commandsurface_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

func TestParserDiagnosticsDescribeStateAcrossSourceUnits(t *testing.T) {
	cases := []struct{ source, kind string }{
		{"printf '%s' 'open", "unclosed_quote"},
		{"printf \"open", "unclosed_quote"},
		{"cat <<'END'\nliteral\n", "unclosed_heredoc"},
		{"if true; then echo ok", "expected_closer"},
		{"(echo ok", "expected_closer"},
		{"echo ok |", "expected_following"},
		{"echo )", "invalid_syntax"},
		{"echo $'escaped'", "unsupported_form"},
		{"\xef\xbb\xbfecho ok", "utf8_bom"},
		{"echo \x00", "nul_byte"},
		{"echo \xff", "invalid_utf8"},
	}
	for _, tc := range cases {
		for _, prefix := range []string{"", "# é\necho earlier\n"} {
			for _, sourced := range []bool{false, true} {
				source := prefix + tc.source
				if tc.kind == "utf8_bom" && prefix != "" {
					continue
				}
				p, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "unit", Sourced: sourced})
				var d *commandsurface.SyntaxDiagnostic
				if p != nil || !errors.As(err, &d) || d.Parser == nil {
					t.Fatalf("%q: program=%v diagnostic=%#v err=%v", source, p, d, err)
				}
				if d.Parser.Kind != tc.kind || d.Parser.Message == "" || d.Sourced != sourced || d.SourceName != "unit" {
					t.Fatalf("%q: %#v / %#v", source, d, d.Parser)
				}
				span := d.Span
				if span.StartByte < 0 || span.EndByte > len(source) || span.EndByte < span.StartByte {
					t.Fatalf("invalid span: %#v", span)
				}
				before := source[:span.StartByte]
				line := strings.Count(before, "\n") + 1
				column := len(before) - strings.LastIndex(before, "\n")
				if span.Line != line || span.Column != column {
					t.Fatalf("%q: location %#v, want %d:%d", source, span, line, column)
				}
			}
		}
	}
}

func TestParserContextPreservesBoundedOriginalBytes(t *testing.T) {
	for _, prefix := range []string{"", "# prior line\n", strings.Repeat("é", 200)} {
		source := prefix + "\nprintf '%s' 'open"
		_, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{})
		var d *commandsurface.SyntaxDiagnostic
		if !errors.As(err, &d) || d.Parser == nil {
			t.Fatalf("missing diagnostic: %v", err)
		}
		context := d.Parser
		span := context.ExcerptAt
		if span.StartByte < 0 || span.EndByte > len(source) || span.EndByte-span.StartByte > 160 || span.StartByte > span.EndByte || !utf8.ValidString(context.Excerpt) || context.Excerpt != source[span.StartByte:span.EndByte] {
			t.Fatalf("context changed original bytes: %#v", context)
		}
		if span.Line != strings.Count(source[:span.StartByte], "\n")+1 || span.Column != span.StartByte-strings.LastIndex(source[:span.StartByte], "\n") {
			t.Fatalf("context coordinates changed: %#v", context)
		}
	}
}
