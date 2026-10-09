package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestReplaceLinesTool(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "line1\nline2\nline3\nline4\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte(seed), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":        "file.txt",
		"start_line":  2,
		"end_line":    3,
		"new_content": "replaced\nblock",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "replace_lines", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "file.txt"))
	testutil.FailErr(t, "read file", err)
	want := "line1\nreplaced\nblock\nline4\n"
	if string(got) != want {
		t.Fatalf("content = %q want %q", got, want)
	}
}

func TestReplaceLinesToolEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "empty.txt"), nil, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":        "empty.txt",
		"start_line":  1,
		"end_line":    1,
		"new_content": "first line",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "replace_lines empty", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "empty.txt"))
	testutil.FailErr(t, "read file", err)
	if string(got) != "first line" {
		t.Fatalf("content = %q", got)
	}
}

func TestReplaceLinesBeyondEOF(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.txt", "start_line": 2, "end_line": 5, "new_content": "x",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "REPLACE_LINES_BEYOND_EOF" {
		t.Fatalf("err = %v want REPLACE_LINES_BEYOND_EOF", err)
	}
}

func TestReplaceLinesInvalidRange(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.txt", "start_line": 3, "end_line": 1, "new_content": "x",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "REPLACE_LINES_INVALID_RANGE" {
		t.Fatalf("err = %v want REPLACE_LINES_INVALID_RANGE", err)
	}
}

func TestReplaceLinesRejectsNonIntegralCoordinates(t *testing.T) {
	_, err := parseLineOperations("a.txt", map[string]any{
		"start_line": 1.5, "end_line": float64(2), "new_content": "x",
	})
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "REPLACE_LINES_INVALID_RANGE" {
		t.Fatalf("err = %v want REPLACE_LINES_INVALID_RANGE", err)
	}
}

func TestReplaceLinesEnforcesAtomicOperationLimit(t *testing.T) {
	operations := make([]any, maxLineOperations+1)
	for i := range operations {
		line := float64(i + 1)
		operations[i] = map[string]any{
			"kind": "replace", "start_line": line, "end_line": line, "new_content": "x",
		}
	}
	_, err := parseLineOperations("a.txt", map[string]any{"operations": operations})
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "REPLACE_LINES_INVALID_RANGE" {
		t.Fatalf("err = %v want REPLACE_LINES_INVALID_RANGE", err)
	}
}

func TestReplaceLinesAtomicOperationsUseOriginalCoordinates(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "one\ntwo\nthree\nfour\nfive\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte(seed), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.txt",
		"operations": []any{
			map[string]any{"kind": "replace", "start_line": float64(2), "end_line": float64(2), "new_content": "TWO\n2.5"},
			map[string]any{"kind": "replace", "start_line": float64(5), "end_line": float64(5), "new_content": "FIVE"},
		},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "replace_lines batch", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "a.txt"))
	testutil.FailErr(t, "read", err)
	if want := "one\nTWO\n2.5\nthree\nfour\nFIVE\n"; string(got) != want {
		t.Fatalf("content = %q want %q", got, want)
	}
}

func TestReplaceLinesTracksOriginalAndFinalBatchSeams(t *testing.T) {
	seam := operationsSeam([]lineOperation{
		{Kind: "replace", StartLine: 1, EndLine: 3, NewContent: ""},
		{Kind: "replace", StartLine: 4, EndLine: 4, NewContent: "four"},
	})
	if seam.OriginalStartLine != 1 || seam.OriginalEndLine != 4 {
		t.Fatalf("original seam = %d-%d want 1-4", seam.OriginalStartLine, seam.OriginalEndLine)
	}
	if seam.FinalStartLine != 1 || seam.FinalEndLine != 1 {
		t.Fatalf("final seam = %d-%d want 1-1", seam.FinalStartLine, seam.FinalEndLine)
	}
}

func TestReplaceLinesShiftIndentIsAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "def run():\n    if ready:\n        first()\n    second()\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.py"), []byte(seed), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.py",
		"operations": []any{
			map[string]any{"kind": "shift_indent", "start_line": float64(4), "end_line": float64(4), "direction": "indent", "prefix": "    "},
		},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "indent", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "a.py"))
	testutil.FailErr(t, "read", err)
	if want := "def run():\n    if ready:\n        first()\n        second()\n"; string(got) != want {
		t.Fatalf("content = %q want %q", got, want)
	}
}

func TestReplaceLinesIndentAndDedentKinds(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "line1\nline2\n    line3\n    line4\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(seed), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}

	// Test indent kind (defaults to 4 spaces)
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "test.txt",
		"operations": []any{
			map[string]any{"kind": "indent", "start_line": 1, "end_line": 2},
		},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "indent", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	testutil.FailErr(t, "read", err)
	if want := "    line1\n    line2\n    line3\n    line4\n"; string(got) != want {
		t.Fatalf("after indent = %q want %q", got, want)
	}

	// Test dedent kind combined with replace
	_, err = tool.Run(context.Background(), map[string]any{
		"path": "test.txt",
		"operations": []any{
			map[string]any{"kind": "replace", "start_line": 1, "end_line": 1, "new_content": "new_line1"},
			map[string]any{"kind": "dedent", "start_line": 2, "end_line": 4},
		},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "dedent and replace", err)
	got, err = os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	testutil.FailErr(t, "read", err)
	if want := "new_line1\nline2\nline3\nline4\n"; string(got) != want {
		t.Fatalf("after dedent and replace = %q want %q", got, want)
	}
}

func TestReplaceLinesDedentMismatchDoesNotWrite(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "one\n  two\nthree\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte(seed), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.txt",
		"operations": []any{
			map[string]any{"kind": "shift_indent", "start_line": float64(2), "end_line": float64(3), "direction": "dedent", "prefix": "  "},
		},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "REPLACE_LINES_INDENT_MISMATCH" {
		t.Fatalf("err = %v want REPLACE_LINES_INDENT_MISMATCH", err)
	}
	got, readErr := os.ReadFile(filepath.Join(tmpDir, "a.txt"))
	testutil.FailErr(t, "read", readErr)
	if string(got) != seed {
		t.Fatalf("failed batch changed file: %q", got)
	}
}

func TestReplaceLinesOverlappingOperationsDoNotWrite(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "one\ntwo\nthree\nfour\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte(seed), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.txt",
		"operations": []any{
			map[string]any{"kind": "replace", "start_line": float64(2), "end_line": float64(3), "new_content": "x"},
			map[string]any{"kind": "shift_indent", "start_line": float64(3), "end_line": float64(4), "direction": "indent", "prefix": "  "},
		},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "REPLACE_LINES_INVALID_RANGE" {
		t.Fatalf("err = %v want REPLACE_LINES_INVALID_RANGE", err)
	}
	got, readErr := os.ReadFile(filepath.Join(tmpDir, "a.txt"))
	testutil.FailErr(t, "read", readErr)
	if string(got) != seed {
		t.Fatalf("overlapping batch changed file: %q", got)
	}
}

func TestEditOldStringNotFoundNearLine(t *testing.T) {
	tmpDir := t.TempDir()
	body := strings.Join([]string{
		"package main",
		"",
		"func main() {}",
		"func helper() {}",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "file.go", "old_string": "func main() { }", "new_string": "x",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "EDIT_OLD_STRING_NOT_FOUND" {
		t.Fatalf("err = %v want EDIT_OLD_STRING_NOT_FOUND", err)
	}
	nearLine, ok := reject.Data["near_line"].(int)
	if !ok || nearLine != 3 {
		t.Fatalf("near_line = %v want 3", reject.Data["near_line"])
	}
	if _, ok := reject.Data["read_hint"].(string); !ok {
		t.Fatalf("read_hint missing: %+v", reject.Data)
	}
}

func TestReplaceLinesSyntaxOverride(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "def greet():\n    print(\"hello\")\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "greet.py"), []byte(seed), 0o644); err != nil {
		testutil.FailErr(t, "write greet.py", err)
	}
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	tc := nativefixture.Context(tmpDir)
	tc.Out = &tools.ToolInvocationOut{}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":                   "greet.py",
		"syntax_override_reason": "intentional incomplete function header",
		"operations": []any{
			map[string]any{
				"kind":        "replace",
				"start_line":  1,
				"end_line":    1,
				"new_content": "def broken(:\n",
			},
		},
	}, tc)
	testutil.FailErr(t, "replace_lines with syntax override", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "greet.py"))
	testutil.FailErr(t, "read file", err)
	if !strings.HasPrefix(string(got), "def broken(:\n") {
		t.Fatalf("expected overridden content, got %q", string(got))
	}
}
