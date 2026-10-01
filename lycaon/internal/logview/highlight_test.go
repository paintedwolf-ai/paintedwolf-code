package logview

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHighlightJSONRespectsColorAndSize(t *testing.T) {
	src := "{\n  \"a\": 1,\n  \"b\": \"x\"\n}"

	t.Setenv("NO_COLOR", "")
	on := NewDisplay(DefaultConfig(), true)
	hl := on.hlJSON(src)
	if !strings.Contains(hl, "\x1b[") {
		t.Error("color on should produce ANSI-highlighted JSON")
	}
	if ansi.Strip(hl) != src {
		t.Errorf("highlighting must preserve the text:\n%q\n%q", ansi.Strip(hl), src)
	}

	if off := NewDisplay(DefaultConfig(), false).hlJSON(src); off != src {
		t.Error("color off should leave content plain")
	}

	big := strings.Repeat("x ", maxHighlightBytes) // over the byte guard
	if on.hlJSON(big) != big {
		t.Error("oversize content should skip highlighting")
	}
}

func TestIndentWrapIsAnsiAware(t *testing.T) {
	on := NewDisplay(DefaultConfig(), true).WithWidth(24)
	src := on.hlJSON("{\n  \"key\": \"a fairly long string value that definitely must wrap several times\"\n}")
	out := on.indentWrap(src, "│ ", 0)
	for _, ln := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(ln); w > 24 {
			t.Errorf("highlighted line exceeds width 24 (%d): %q", w, ansi.Strip(ln))
		}
	}
}
