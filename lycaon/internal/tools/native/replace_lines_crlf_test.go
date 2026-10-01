package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// Replacement lines inherit the document's line endings.
func TestReplaceLinesPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "crlf.txt")
	const original = "one\r\ntwo\r\nthree\r\n"
	testutil.FailErr(t, "seed file", os.WriteFile(abs, []byte(original), 0o644))

	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	if _, err := tool.Run(context.Background(), map[string]any{
		"path": "crlf.txt", "start_line": 2, "end_line": 2, "new_content": "TWO\nEXTRA",
	}, nativefixture.Context(dir)); err != nil {
		testutil.FailErr(t, "replace_lines", err)
	}

	got := readFileString(t, abs)
	const want = "one\r\nTWO\r\nEXTRA\r\nthree\r\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Fatalf("a bare LF survived in a CRLF file: %q", got)
	}
}

// An LF file gains no carriage returns from the same code path.
func TestReplaceLinesKeepsLFFileLF(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "lf.txt")
	testutil.FailErr(t, "seed file", os.WriteFile(abs, []byte("one\ntwo\nthree\n"), 0o644))

	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	if _, err := tool.Run(context.Background(), map[string]any{
		"path": "lf.txt", "start_line": 2, "end_line": 2, "new_content": "TWO",
	}, nativefixture.Context(dir)); err != nil {
		testutil.FailErr(t, "replace_lines", err)
	}
	if got, want := readFileString(t, abs), "one\nTWO\nthree\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// A CRLF file with no final newline keeps both facts: CRLF between lines, and
// no terminator after the last one.
func TestReplaceLinesCRLFWithoutTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "crlf-tail.txt")
	testutil.FailErr(t, "seed file", os.WriteFile(abs, []byte("one\r\ntwo"), 0o644))

	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	if _, err := tool.Run(context.Background(), map[string]any{
		"path": "crlf-tail.txt", "start_line": 2, "end_line": 2, "new_content": "TWO",
	}, nativefixture.Context(dir)); err != nil {
		testutil.FailErr(t, "replace_lines", err)
	}
	if got, want := readFileString(t, abs), "one\r\nTWO"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// shift_indent walks the same splice, so re-indented lines keep their
// terminators.
func TestReplaceLinesShiftIndentPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "indent.txt")
	testutil.FailErr(t, "seed file", os.WriteFile(abs, []byte("a\r\nb\r\nc\r\n"), 0o644))

	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	if _, err := tool.Run(context.Background(), map[string]any{
		"path": "indent.txt",
		"operations": []any{map[string]any{
			"kind": "shift_indent", "start_line": 1, "end_line": 2,
			"direction": "indent", "prefix": "  ",
		}},
	}, nativefixture.Context(dir)); err != nil {
		testutil.FailErr(t, "replace_lines", err)
	}
	if got, want := readFileString(t, abs), "  a\r\n  b\r\nc\r\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func readFileString(t *testing.T, abs string) string {
	t.Helper()
	raw, err := os.ReadFile(abs) // #nosec G304 -- test-owned temp path.
	testutil.FailErr(t, "read back", err)
	return string(raw)
}
