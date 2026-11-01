package search

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/textfile"
)

func TestRgJSONMatchLimitsKeepExactBinaryAndContextExtents(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		binary        int
		extents       []int
	}{
		{"LF before NUL", "alpha\n\x00tail\n", 6, []int{6, 6, 6, 12}},
		{"NUL before LF", "alpha\x00\n\x00tail\n", 5, []int{6, 6, 6, 6, 13}},
		{"unterminated binary context", "alpha\x00tail", 5, []int{6, 6, 10, 10}},
		{"plain context", "alpha\nnext\n", -1, []int{6, 6, 11, 11}},
		{"unterminated match", "a\x00alpha", 1, []int{7, 7, 7, 7}},
	} {
		for after, extent := range tc.extents {
			t.Run(fmt.Sprintf("%s/after_%d", tc.name, after), func(t *testing.T) {
				h := newHarness(t, map[string]string{"input": tc.content})
				argv := []string{"rg", "--json", "-m", "1", "-A", strconv.Itoa(after), "alpha", "input"}
				got := h.exec("", argv...)
				if got.err != nil || got.result.Status != 0 {
					t.Fatalf("search status %d: %v", got.result.Status, got.err)
				}
				ended := false
				for _, line := range strings.Split(strings.TrimSpace(got.stdout), "\n") {
					var event struct {
						Type string
						Data struct {
							Binary *int `json:"binary_offset"`
							Stats  struct {
								Searched int `json:"bytes_searched"`
							}
						}
					}
					if err := json.Unmarshal([]byte(line), &event); err != nil {
						t.Fatal(err)
					}
					if event.Type == "end" {
						ended = true
						if event.Data.Stats.Searched != extent || (event.Data.Binary == nil) != (tc.binary < 0) || (tc.binary >= 0 && *event.Data.Binary != tc.binary) {
							t.Fatalf("wrong source extent: %s", line)
						}
					}
				}
				if !ended {
					t.Fatalf("missing end event: %s", got.stdout)
				}
				pins := h.pins.pins[got.execution]
				if len(pins) != 1 || pins[0].ContentSHA256 != textfile.SHA256([]byte(tc.content)) {
					t.Fatalf("source identity changed: %+v", pins)
				}
				for _, ref := range references(t) {
					if ref.kind != "rg" {
						continue
					}
					want, status := runReference(t, ref, h.root, argv[1:])
					if status != got.result.Status || normalizeElapsed(want) != normalizeElapsed(got.stdout) {
						t.Fatalf("reference differs:\n%s\nnative:\n%s", want, got.stdout)
					}
				}
			})
		}
	}
}
