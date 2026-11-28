package promptrender

import (
	"strings"
	"testing"
)

func TestProseDriftPreservesASCIIEmptyListSemantics(t *testing.T) {
	t.Parallel()
	// ASCII whitespace around an empty list marker is flagged.
	for _, line := range []string{"-", "  *  ", "\t1.\t", " 12. "} {
		drift := ProseDrift(line)
		if len(drift) != 1 || !strings.Contains(drift[0], "an empty list item") {
			t.Errorf("ProseDrift(%q) = %v, want single empty list item drift", line, drift)
		}
	}
	// Non-ASCII whitespace (e.g. NBSP) did not match Go's RE2 \s and must not be flagged.
	for _, line := range []string{"\u00a0-", "-\u00a0", "\u00a01.\u00a0"} {
		for _, item := range ProseDrift(line) {
			if strings.Contains(item, "an empty list item") {
				t.Errorf("ProseDrift(%q) unexpectedly flagged non-ASCII whitespace as empty list item: %v", line, item)
			}
		}
	}
}
