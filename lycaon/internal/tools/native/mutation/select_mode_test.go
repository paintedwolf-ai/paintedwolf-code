package mutation

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// A dispatcher's preflight asks the owner which mode a child's argv selects;
// the answer is the mode Prepare selects for the same argv.
func TestSelectModeNamesWhatPrepareSelects(t *testing.T) {
	h := newHarness(t, nil)
	for source, want := range map[string]string{"tar -tf a.tar": "listing-or-stream-decode", "tar -xf a.tar": "create-extract-compress-decompress-to-path", "unzip -l a.zip": "listing-or-stream-decode", "gzip -dc a.gz": "listing-or-stream-decode", "gzip -d a.gz": "create-extract-compress-decompress-to-path", "gunzip": "listing-or-stream-decode"} {
		var argv commandprogram.Fields
		for _, word := range strings.Fields(source) {
			argv = append(argv, commandprogram.Field{Value: word})
		}
		if got, known := h.owners.SelectMode(argv); !known || got != want {
			t.Errorf("%s: mode %q (known %v), want %q", source, got, known, want)
		}
	}
}
