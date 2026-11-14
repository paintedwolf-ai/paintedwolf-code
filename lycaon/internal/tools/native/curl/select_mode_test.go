package curl

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// A dispatcher's preflight asks the owner which mode a child's argv selects;
// the answer is the mode Prepare selects for the same argv.
func TestSelectModeNamesWhatPrepareSelects(t *testing.T) {
	h := newHarness(t)
	for source, want := range map[string]string{"curl https://example.test/": "request", "curl -o /dev/null https://example.test/": "request", "curl -o page.html https://example.test/": "-o--D--c-to-file"} {
		var argv commandprogram.Fields
		for _, word := range strings.Fields(source) {
			argv = append(argv, commandprogram.Field{Value: word})
		}
		if got, known := h.owners.SelectMode(argv); !known || got != want {
			t.Errorf("%s: mode %q (known %v), want %q", source, got, known, want)
		}
	}
}
