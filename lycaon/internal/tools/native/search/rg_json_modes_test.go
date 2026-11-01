package search

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/textfile"
)

type jsonModeRecord struct {
	Text         string
	Line, Offset int
	Spans        [][2]int
}

func TestRgJSONModesPreserveRecordBoundariesAndSourceIdentity(t *testing.T) {
	const content = "a\x00alpha\nalpha\x00tail\n"
	binary := []jsonModeRecord{{"alpha\n", 2, 2, [][2]int{{0, 5}}}, {"alpha\n", 3, 8, [][2]int{{0, 5}}}}
	text := []jsonModeRecord{{"a\x00alpha\n", 1, 0, [][2]int{{2, 7}}}, {"alpha\x00tail\n", 2, 8, [][2]int{{0, 5}}}}
	null := []jsonModeRecord{{"alpha\nalpha\x00", 2, 2, [][2]int{{0, 5}, {6, 11}}}}
	for _, tc := range []struct {
		name     string
		flags    []string
		records  []jsonModeRecord
		binary   bool
		searched int
	}{
		{"binary", nil, binary, true, 19},
		{"text", []string{"-a"}, text, false, 19},
		{"null data", []string{"--null-data"}, null, false, 19},
		{"null text", []string{"--null-data", "-a"}, null, false, 19},
		{"JSON binary search", []string{"-I"}, binary, true, 19},
		{"limited binary", []string{"-m", "1"}, binary[:1], true, 8},
		{"limited text", []string{"-a", "-m", "1"}, text[:1], false, 8},
		{"limited null", []string{"--null-data", "-m", "1"}, null, false, 14},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{"input": content})
			argv := append(append([]string{"rg", "--json"}, tc.flags...), "alpha", "input")
			got := h.exec("", argv...)
			if got.err != nil || got.result.Status != 0 {
				t.Fatalf("search status %d: %v", got.result.Status, got.err)
			}
			var records []jsonModeRecord
			ended := false
			for _, line := range strings.Split(strings.TrimSpace(got.stdout), "\n") {
				var event struct {
					Type string
					Data struct {
						Lines      struct{ Text string }
						Line       int  `json:"line_number"`
						Offset     int  `json:"absolute_offset"`
						Binary     *int `json:"binary_offset"`
						Submatches []struct{ Start, End int }
						Stats      struct {
							Searched int `json:"bytes_searched"`
						}
					}
				}
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if event.Type == "match" {
					record := jsonModeRecord{Text: event.Data.Lines.Text, Line: event.Data.Line, Offset: event.Data.Offset}
					for _, span := range event.Data.Submatches {
						record.Spans = append(record.Spans, [2]int{span.Start, span.End})
					}
					records = append(records, record)
				}
				if event.Type == "end" {
					ended = true
					if (event.Data.Binary != nil) != tc.binary || (tc.binary && *event.Data.Binary != 1) || event.Data.Stats.Searched != tc.searched {
						t.Fatalf("wrong source extent: %s", line)
					}
				}
			}
			if !ended || !reflect.DeepEqual(records, tc.records) {
				t.Fatalf("records = %#v, want %#v", records, tc.records)
			}
			pins := h.pins.pins[got.execution]
			if len(pins) != 1 || pins[0].ContentSHA256 != textfile.SHA256([]byte(content)) {
				t.Fatalf("source bytes changed: %+v", pins)
			}
			for _, ref := range references(t) {
				if ref.kind != "rg" {
					continue
				}
				want, status := runReference(t, ref, h.root, argv[1:])
				if status != got.result.Status || normalizeElapsed(want) != normalizeElapsed(got.stdout) {
					t.Fatalf("reference %s differs:\n%s\nnative:\n%s", ref.path, want, got.stdout)
				}
			}
		})
	}
}
