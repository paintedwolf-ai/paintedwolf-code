package fileoutline

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiffOutlineFileBoundaries(t *testing.T) {
	text := "diff --git a/old.txt b/new.txt\n--- a/old.txt\n+++ b/new.txt\n@@ -1 +1 @@\n--- looks like a header\n+++ also content\n" +
		"diff --git a/gone.txt b/gone.txt\n--- a/gone.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n" +
		"--- /dev/null\n+++ made.txt\t2026-09-15\n@@ -0,0 +1 @@\n+new\n" +
		"diff --git \"a/tab\\tname\" \"b/tab\\tname\"\nBinary files differ\n" +
		"diff --cc merged.txt\nindex 123,234..345\n"
	out := AnalyzeText(t.Context(), "changes.patch", []byte(text))
	want := []string{"new.txt", "gone.txt", "made.txt", "tab\tname", "merged.txt"}
	if len(out.Symbols) != len(want) {
		t.Fatalf("symbols = %+v", out.Symbols)
	}
	for i, name := range want {
		if out.Symbols[i].Name != name {
			t.Fatalf("symbol %d = %+v, want %q", i, out.Symbols[i], name)
		}
		if i > 0 && out.Symbols[i].Line <= out.Symbols[i-1].Line {
			t.Fatal("file boundaries are out of order")
		}
	}
	if out.DefinitionError() != nil || out.Source != "diff" {
		t.Fatalf("analysis = %+v", out)
	}
}

func TestDiffOutlineRecordedPatchUnderContention(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../../lycaon-den/patches/overlayscrollbars@2.16.0.patch")
	testutil.FailErr(t, "read recorded patch", err)
	for n := 0; n < 8; n++ {
		t.Run(string(rune('a'+n)), func(t *testing.T) {
			t.Parallel()
			// Distinct content avoids coalescing away the concurrent parsing work.
			out := analyzeText(t.Context(), "changes.diff", []byte(strings.Repeat(string(data), n+1)))
			if len(out.Symbols) != 4*(n+1) || out.DefinitionError() != nil {
				t.Fatalf("outline: symbols=%d, error=%v", len(out.Symbols), out.DefinitionError())
			}
		})
	}
}

func BenchmarkDiffOutline(b *testing.B) {
	text := strings.Repeat("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n", 1000)
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		var result Result
		outlineDiff(&result, text)
	}
}
