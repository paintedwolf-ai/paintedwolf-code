package hostmarker_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

func TestNumberedLinesRoundTrip(t *testing.T) {
	page := []string{"package main", "", "func main() {", "\tprintln(\"hello\")", "}"}
	lines := strings.Split(hostmarker.FormatNumberedLines(page, 10), "\n")
	if len(lines) != len(page) {
		t.Fatalf("rendered %d lines, want %d", len(lines), len(page))
	}
	for i, line := range lines {
		n, text, ok := hostmarker.ParseNumberedLine(line)
		if !ok || n != 10+i || text != page[i] {
			t.Fatalf("line %d %q parsed as (%d, %q, %v), want (%d, %q, true)", i, line, n, text, ok, 10+i, page[i])
		}
	}
}

func TestNumberedLinesRenderExactly(t *testing.T) {
	cases := []struct {
		page  []string
		start int
		want  string
	}{
		{[]string{""}, 2, "     2: "},
		{[]string{"12: x"}, 1, "     1: 12: x"},
		{[]string{"big line number"}, 1234567, "1234567: big line number"},
		{nil, 1, ""},
	}
	for _, c := range cases {
		if got := hostmarker.FormatNumberedLines(c.page, c.start); got != c.want {
			t.Fatalf("FormatNumberedLines(%q, %d) = %q, want %q", c.page, c.start, got, c.want)
		}
	}
}

func TestParseNumberedLineReadsReleasedBodyForms(t *testing.T) {
	cases := []struct {
		line string
		n    int
		text string
	}{
		{"     1: 12: x", 1, "12: x"},
		{"7\tif x {", 7, "if x {"},
		{"7|if x {", 7, "if x {"},
		{"42:", 42, ""},
	}
	for _, c := range cases {
		n, text, ok := hostmarker.ParseNumberedLine(c.line)
		if !ok || n != c.n || text != c.text {
			t.Fatalf("ParseNumberedLine(%q) = (%d, %q, %v), want (%d, %q, true)", c.line, n, text, ok, c.n, c.text)
		}
	}
}

func TestParseNumberedLineRejectsUnnumberedText(t *testing.T) {
	for _, line := range []string{"not numbered", "  foo: bar", ": empty number", ""} {
		if _, _, ok := hostmarker.ParseNumberedLine(line); ok {
			t.Fatalf("ParseNumberedLine(%q) accepted unnumbered text", line)
		}
	}
}
