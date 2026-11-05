package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
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

func TestCommandToolRequiresRunner(t *testing.T) {
	tool := &command.CommandTool{}
	_, err := tool.Run(context.Background(), map[string]any{"command": "echo hi"}, nativefixture.Context(t.TempDir()))
	if err == nil {
		t.Fatalf("err = %v", err)
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
