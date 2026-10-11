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
	"github.com/lycaon/lycaon/internal/tools/native/command"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
)

func TestWriteToolRejectsBinaryContent(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "bad.py",
		"content": "a\x00b",
	}, nativefixture.AgentContext(tmpDir, "implement"))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "WRITE_BINARY_DENIED" {
		t.Fatalf("err = %v want WRITE_BINARY_DENIED", err)
	}
}

func TestWriteToolAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	out := &tools.ToolInvocationOut{}
	ctx := nativefixture.AgentContext(tmpDir, "implement")
	ctx.Effects.Out = out
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "test.txt",
		"content": "test content",
	}, ctx)
	testutil.FailErr(t, "tool.Run failed", err)
	content, err := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	testutil.FailErr(t, "read file", err)
	if string(content) != "test content" {
		t.Fatalf("content = %q", content)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "test.txt.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file should not remain")
	}
	if out.FileEdit == nil {
		t.Fatal("expected file_edit capture")
	}
	if out.FileEdit.Path != "test.txt" || out.FileEdit.After != "test content" {
		t.Fatalf("file_edit = %+v", out.FileEdit)
	}
	if out.FileEdit.Before != nil {
		t.Fatalf("new file before = %v", out.FileEdit.Before)
	}
}

func TestWriteToolRejectsParseRegression(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "package p\n\nfunc A() int {\n\treturn 1\n}\n"
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(seed), 0o644))
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "a.go",
		"content": "package p\n\nfunc A() int {\n\treturn (\n}\n",
	}, nativefixture.AgentContext(tmpDir, "implement"))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "MUTATION_BROKE_PARSE" {
		t.Fatalf("err = %v want MUTATION_BROKE_PARSE", err)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "a.go"))
	testutil.FailErr(t, "read", err)
	if string(got) != seed {
		t.Fatalf("broken overwrite must not apply: %q", got)
	}
}

func TestWriteToolAppend(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(tmpDir, "big.html"), []byte("<html>\n"), 0o644))
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	out := &tools.ToolInvocationOut{}
	ctx := nativefixture.AgentContext(tmpDir, "implement")
	ctx.Effects.Out = out
	receipt, err := tool.Run(context.Background(), map[string]any{
		"path":    "big.html",
		"content": "<body></body>\n",
		"append":  true,
	}, ctx)
	testutil.FailErr(t, "append", err)
	if !strings.Contains(receipt, "Appended 14 bytes") || !strings.Contains(receipt, "file now 21 bytes") {
		t.Fatalf("receipt = %q", receipt)
	}
	content, err := os.ReadFile(filepath.Join(tmpDir, "big.html"))
	testutil.FailErr(t, "read file", err)
	if string(content) != "<html>\n<body></body>\n" {
		t.Fatalf("content = %q", content)
	}
	if out.FileEdit == nil || out.FileEdit.Before == nil || *out.FileEdit.Before != "<html>\n" {
		t.Fatalf("file_edit = %+v", out.FileEdit)
	}
}

func TestWriteToolAppendRejectsMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "missing.txt",
		"content": "chunk",
		"append":  true,
	}, nativefixture.AgentContext(tmpDir, "implement"))
	if err == nil {
		t.Fatal("expected reject for append to missing file")
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "missing.txt")); !os.IsNotExist(statErr) {
		t.Fatal("append must not create the file")
	}
}

func TestWriteToolCaptureOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("old"), 0o644); err != nil {
		testutil.FailErr(t, "seed file", err)
	}
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	out := &tools.ToolInvocationOut{}
	ctx := nativefixture.AgentContext(tmpDir, "implement")
	ctx.Effects.Out = out
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "a.txt",
		"content": "new",
	}, ctx)
	testutil.FailErr(t, "tool.Run failed", err)
	if out.FileEdit == nil || out.FileEdit.Before == nil || *out.FileEdit.Before != "old" {
		t.Fatalf("file_edit before = %+v", out.FileEdit)
	}
	if out.FileEdit.After != "new" {
		t.Fatalf("file_edit after = %q", out.FileEdit.After)
	}
}

func TestEditToolOldStringNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("package main"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "file.go", "old_string": "missing", "new_string": "x",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "EDIT_OLD_STRING_NOT_FOUND" {
		t.Fatalf("err = %v want EDIT_OLD_STRING_NOT_FOUND", err)
	}
}

func TestEditToolOldStringAmbiguous(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("foo bar foo"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "file.go", "old_string": "foo", "new_string": "baz",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "EDIT_OLD_STRING_AMBIGUOUS" {
		t.Fatalf("err = %v want EDIT_OLD_STRING_AMBIGUOUS", err)
	}
}

func TestEditToolReplaceAll(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("package main\n\nvar foo = \"foo foo\"\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "file.go", "old_string": "foo", "new_string": "qux", "replace_all": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "edit replace_all", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "file.go"))
	testutil.FailErr(t, "read back", err)
	if string(got) != "package main\n\nvar qux = \"qux qux\"\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(out, "3 occurrence") {
		t.Fatalf("result should report 3 occurrences: %q", out)
	}
}

func TestEditToolReplaceAllFalseStillAmbiguous(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("foo bar foo"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "file.go", "old_string": "foo", "new_string": "baz", "replace_all": false,
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "EDIT_OLD_STRING_AMBIGUOUS" {
		t.Fatalf("err = %v want EDIT_OLD_STRING_AMBIGUOUS", err)
	}
}

func TestCommandToolRequiresRunner(t *testing.T) {
	tool := &command.CommandTool{}
	_, err := tool.Run(context.Background(), map[string]any{"command": "echo hi"}, nativefixture.Context(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditTool(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("package main\n\nfunc main() {}"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":       "file.go",
		"old_string": "func main() {}",
		"new_string": "func main() { fmt.Println(\"hello\") }",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "tool.Run failed", err)
	content, _ := os.ReadFile(filepath.Join(tmpDir, "file.go"))
	if !strings.Contains(string(content), "fmt.Println") {
		t.Fatalf("content = %q", content)
	}
}

func TestPathEscapeBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "../secret.txt"}, nativefixture.Context(tmpDir))
	if err == nil {
		t.Fatal("expected escape error")
	}
}
