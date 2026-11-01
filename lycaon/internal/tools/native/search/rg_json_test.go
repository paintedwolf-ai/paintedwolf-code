package search

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/textfile"
	"regexp"
	"strings"
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
		{"binary bytes delimit records", []string{"rg", "--json", "alpha", "bin"}, "",
			`{"type":"begin","data":{"path":{"text":"bin"}}}
{"type":"match","data":{"path":{"text":"bin"},"lines":{"text":"alpha\n"},"line_number":2,"absolute_offset":2,"submatches":[{"match":{"text":"alpha"},"start":0,"end":5}]}}
{"type":"end","data":{"path":{"text":"bin"},"binary_offset":1,"stats":{"elapsed":{},"searches":1,"searches_with_match":1,"bytes_searched":8,"bytes_printed":219,"matched_lines":1,"matches":1}}}
{"data":{"elapsed_total":{},"stats":{"bytes_printed":219,"bytes_searched":8,"elapsed":{},"matched_lines":1,"matches":1,"searches":1,"searches_with_match":1}},"type":"summary"}
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

func TestRgJSONBinaryRecordsKeepOriginalOffsetsAndInput(t *testing.T) {
	const content = "a\x00alpha\nalpha\x00tail\n"
	h := newHarness(t, map[string]string{"input": content})
	result := h.exec("", "rg", "--json", "alpha", "input")
	if result.err != nil || result.result.Status != 0 {
		t.Fatalf("search: %v status %d", result.err, result.result.Status)
	}
	lines := strings.Split(strings.TrimSpace(result.stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("events: %s", result.stdout)
	}
	for i := range 2 {
		var event struct {
			Data struct {
				Lines      struct{ Text string }
				LineNumber int                        `json:"line_number"`
				Offset     int                        `json:"absolute_offset"`
				Matches    []struct{ Start, End int } `json:"submatches"`
			}
		}
		if err := json.Unmarshal([]byte(lines[i+1]), &event); err != nil {
			t.Fatal(err)
		}
		if event.Data.Lines.Text != "alpha\n" || event.Data.LineNumber != i+2 || event.Data.Offset != 2+i*6 || len(event.Data.Matches) != 1 || event.Data.Matches[0].Start != 0 || event.Data.Matches[0].End != 5 {
			t.Fatalf("event %s", lines[i+1])
		}
	}
	var end struct {
		Data struct {
			Binary int `json:"binary_offset"`
			Stats  struct {
				Searched int `json:"bytes_searched"`
			}
		}
	}
	if err := json.Unmarshal([]byte(lines[3]), &end); err != nil {
		t.Fatal(err)
	}
	if end.Data.Binary != 1 || end.Data.Stats.Searched != len(content) {
		t.Fatalf("end event %s", lines[3])
	}
	pins := h.pins.pins[result.execution]
	if len(pins) != 1 || pins[0].ContentSHA256 != textfile.SHA256([]byte(content)) {
		t.Fatalf("binary source identity changed: %+v", pins)
	}
}
