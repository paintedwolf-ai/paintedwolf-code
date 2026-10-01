package native

import (
	"fmt"
	"strings"
	"testing"
)

func numberedFile(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	return b.String()
}

func TestSeamContextLargeInsertionTwoWindows(t *testing.T) {
	out := seamContext(numberedFile(100), 20, 40)
	if !strings.Contains(out, "18\tline18") || !strings.Contains(out, "22\tline22") {
		t.Fatalf("top seam window missing: %q", out)
	}
	if !strings.Contains(out, "57\tline57") || !strings.Contains(out, "61\tline61") {
		t.Fatalf("bottom seam window missing: %q", out)
	}
	if !strings.Contains(out, "\n…\n") {
		t.Fatalf("expected window separator: %q", out)
	}
	if strings.Contains(out, "30\t") {
		t.Fatalf("interior lines should be elided: %q", out)
	}
}

func TestSeamContextSmallInsertionMergesWindows(t *testing.T) {
	out := seamContext(numberedFile(30), 10, 3)
	if strings.Contains(out, "…") {
		t.Fatalf("windows should merge for small insertions: %q", out)
	}
	for _, want := range []string{"8\tline8", "14\tline14"} {
		if !strings.Contains(out, want) {
			t.Fatalf("merged window missing %q: %q", want, out)
		}
	}
}

func TestSeamContextDeletionShowsJoin(t *testing.T) {
	out := seamContext(numberedFile(20), 8, 0)
	for _, want := range []string{"6\tline6", "10\tline10"} {
		if !strings.Contains(out, want) {
			t.Fatalf("join window missing %q: %q", want, out)
		}
	}
}

func TestSeamContextClampsAtFileEdges(t *testing.T) {
	out := seamContext(numberedFile(5), 1, 5)
	if !strings.HasPrefix(out, "Seam context (after edit):\n1\tline1") {
		t.Fatalf("expected clamp at line 1: %q", out)
	}
	if !strings.Contains(out, "5\tline5") {
		t.Fatalf("expected clamp at last line: %q", out)
	}
	if strings.Contains(out, "0\t") || strings.Contains(out, "6\t") {
		t.Fatalf("window escaped file bounds: %q", out)
	}
}

func TestSeamContextEmptyContent(t *testing.T) {
	if out := seamContext("", 1, 0); out != "" {
		t.Fatalf("expected empty seam context for empty content, got %q", out)
	}
}

func TestSeamContextTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("a", 500)
	out := seamContext("short\n"+long+"\nshort\n", 2, 1)
	if strings.Contains(out, long) {
		t.Fatalf("long line should be truncated: %d bytes", len(out))
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("expected truncation marker: %q", out)
	}
}
