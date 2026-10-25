package literal

import (
	"strings"
	"testing"
)

func TestFileBoundedHeaderPreservesSplitUTF8AndRejectsMalformedTail(t *testing.T) {
	// The bounded identification prefix stops partway through the final scalar.
	prefix := "é" + strings.Repeat("a", 64<<10)
	h := newHarness(t, map[string]string{
		"split":      prefix + "😀" + strings.Repeat("x", 64),
		"malformed":  prefix + "\xff" + strings.Repeat("x", 64),
		"incomplete": prefix + "\xf0\x9f",
	})
	h.exec("", "file", "-b", "split").expect(t, "bounded Unicode header", "UTF-8 Unicode text\n", "", 0)
	h.exec("", "file", "-b", "malformed").expect(t, "malformed Unicode header", "data\n", "", 0)
	h.exec("", "file", "-b", "incomplete").expect(t, "incomplete final scalar", "data\n", "", 0)
}
