package search

import (
	"regexp"
	"testing"
)

// elapsedFields hides the measured durations; every other byte is compared.
var elapsedFields = regexp.MustCompile(`"(elapsed|elapsed_total)":\{[^}]*\}`)

func normalizeElapsed(s string) string { return elapsedFields.ReplaceAllString(s, `"$1":{}`) }

var jsonTree = map[string]string{
	"a.txt":     "one\ntwo alpha\nthree\nalpha alpha\n",
	"sub/b.txt": "x\n",
	"esc":       "a\tb \"q\" \\ alpha\x01\n",
	"inv":       "caf\xc3\xa9 \xff alpha\n",
	"bin":       "a\x00alpha\n",
}

func TestRgJSONMessages(t *testing.T) {
	h := newHarness(t, jsonTree)
	for _, c := range []struct {
		name   string
		argv   []string
		stdin  string
		stdout string
		status int
	}{
		{"matches with submatches", []string{"rg", "--json", "alpha", "a.txt"}, "",
			`{"type":"begin","data":{"path":{"text":"a.txt"}}}
{"type":"match","data":{"path":{"text":"a.txt"},"lines":{"text":"two alpha\n"},"line_number":2,"absolute_offset":4,"submatches":[{"match":{"text":"alpha"},"start":4,"end":9}]}}
{"type":"match","data":{"path":{"text":"a.txt"},"lines":{"text":"alpha alpha\n"},"line_number":4,"absolute_offset":20,"submatches":[{"match":{"text":"alpha"},"start":0,"end":5},{"match":{"text":"alpha"},"start":6,"end":11}]}}
{"type":"end","data":{"path":{"text":"a.txt"},"binary_offset":null,"stats":{"elapsed":{},"searches":1,"searches_with_match":1,"bytes_searched":32,"bytes_printed":453,"matched_lines":2,"matches":3}}}
{"data":{"elapsed_total":{},"stats":{"bytes_printed":453,"bytes_searched":32,"elapsed":{},"matched_lines":2,"matches":3,"searches":1,"searches_with_match":1}},"type":"summary"}
`, 0},
		{"binary bytes stay in their record", []string{"rg", "--json", "alpha", "bin"}, "",
			`{"type":"begin","data":{"path":{"text":"bin"}}}
{"type":"match","data":{"path":{"text":"bin"},"lines":{"text":"a\u0000alpha\n"},"line_number":1,"absolute_offset":0,"submatches":[{"match":{"text":"alpha"},"start":2,"end":7}]}}
{"type":"end","data":{"path":{"text":"bin"},"binary_offset":1,"stats":{"elapsed":{},"searches":1,"searches_with_match":1,"bytes_searched":1,"bytes_printed":226,"matched_lines":1,"matches":1}}}
{"data":{"elapsed_total":{},"stats":{"bytes_printed":226,"bytes_searched":1,"elapsed":{},"matched_lines":1,"matches":1,"searches":1,"searches_with_match":1}},"type":"summary"}
`, 0},
		{"context", []string{"rg", "--json", "-C1", "two", "a.txt"}, "",
			`{"type":"begin","data":{"path":{"text":"a.txt"}}}
{"type":"context","data":{"path":{"text":"a.txt"},"lines":{"text":"one\n"},"line_number":1,"absolute_offset":0,"submatches":[]}}
{"type":"match","data":{"path":{"text":"a.txt"},"lines":{"text":"two alpha\n"},"line_number":2,"absolute_offset":4,"submatches":[{"match":{"text":"two"},"start":0,"end":3}]}}
{"type":"context","data":{"path":{"text":"a.txt"},"lines":{"text":"three\n"},"line_number":3,"absolute_offset":14,"submatches":[]}}
{"type":"end","data":{"path":{"text":"a.txt"},"binary_offset":null,"stats":{"elapsed":{},"searches":1,"searches_with_match":1,"bytes_searched":32,"bytes_printed":486,"matched_lines":1,"matches":1}}}
{"data":{"elapsed_total":{},"stats":{"bytes_printed":486,"bytes_searched":32,"elapsed":{},"matched_lines":1,"matches":1,"searches":1,"searches_with_match":1}},"type":"summary"}
`, 0},
		{"no match is only a summary", []string{"rg", "--json", "zz", "a.txt"}, "",
			`{"data":{"elapsed_total":{},"stats":{"bytes_printed":0,"bytes_searched":32,"elapsed":{},"matched_lines":0,"matches":0,"searches":1,"searches_with_match":0}},"type":"summary"}
`, 1},
		{"escapes and replacement", []string{"rg", "--json", "-r", "X", "alpha", "esc"}, "",
			`{"type":"begin","data":{"path":{"text":"esc"}}}
{"type":"match","data":{"path":{"text":"esc"},"lines":{"text":"a\tb \"q\" \\ alpha\u0001\n"},"line_number":1,"absolute_offset":0,"submatches":[{"match":{"text":"alpha"},"replacement":{"text":"X"},"start":10,"end":15}]}}
{"type":"end","data":{"path":{"text":"esc"},"binary_offset":null,"stats":{"elapsed":{},"searches":1,"searches_with_match":1,"bytes_searched":17,"bytes_printed":268,"matched_lines":1,"matches":1}}}
{"data":{"elapsed_total":{},"stats":{"bytes_printed":268,"bytes_searched":17,"elapsed":{},"matched_lines":1,"matches":1,"searches":1,"searches_with_match":1}},"type":"summary"}
`, 0},
		{"standard input", []string{"rg", "--json", "alpha"}, "x alpha",
			`{"type":"begin","data":{"path":{"text":"<stdin>"}}}
{"type":"match","data":{"path":{"text":"<stdin>"},"lines":{"text":"x alpha"},"line_number":1,"absolute_offset":0,"submatches":[{"match":{"text":"alpha"},"start":2,"end":7}]}}
{"type":"end","data":{"path":{"text":"<stdin>"},"binary_offset":null,"stats":{"elapsed":{},"searches":1,"searches_with_match":1,"bytes_searched":7,"bytes_printed":227,"matched_lines":1,"matches":1}}}
{"data":{"elapsed_total":{},"stats":{"bytes_printed":227,"bytes_searched":7,"elapsed":{},"matched_lines":1,"matches":1,"searches":1,"searches_with_match":1}},"type":"summary"}
`, 0},
		{"count ignores json", []string{"rg", "--json", "-c", "alpha", "a.txt"}, "", "2\n", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.exec(c.stdin, c.argv...)
			if r.err != nil || normalizeElapsed(r.stdout) != c.stdout || r.result.Status != c.status {
				t.Fatalf("stdout %q status %d err %v\nwant   %q status %d", normalizeElapsed(r.stdout), r.result.Status, r.err, c.stdout, c.status)
			}
		})
	}
}
