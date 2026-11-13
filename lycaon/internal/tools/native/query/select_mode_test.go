package query

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// A dispatcher's preflight asks the owner which mode a child's argv selects;
// the answer is the mode Prepare selects for the same argv.
func TestSelectModeNamesWhatPrepareSelects(t *testing.T) {
	h := newHarness(t, nil)
	for source, want := range map[string]string{"yq .a f.yaml": "query", "yq -i .a=1 f.yaml": "-i"} {
		var argv commandprogram.Fields
		for _, word := range strings.Fields(source) {
			argv = append(argv, commandprogram.Field{Value: word})
		}
		if got, known := h.owners.SelectMode(argv); !known || got != want {
			t.Errorf("%s: mode %q (known %v), want %q", source, got, known, want)
		}
	}
}
