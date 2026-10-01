package survey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestStatTool(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "run.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &StatTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"run.sh"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "stat", err)
	if !strings.Contains(out, `"mode":"755"`) || !strings.Contains(out, `"is_dir":false`) {
		t.Fatalf("stat out = %q", out)
	}
}

func TestWcTool(t *testing.T) {
	tmpDir := t.TempDir()
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "foo.go"), []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &WcTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"foo.go"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "wc", err)
	if !strings.Contains(out, `"lines":3`) || !strings.Contains(out, `"bytes":`) {
		t.Fatalf("wc out = %q", out)
	}
}

func TestWcToolSkipsBinary(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "bin.dat"), []byte("a\x00b"), 0o644); err != nil {
		testutil.FailErr(t, "write binary", err)
	}
	tool := &WcTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"bin.dat"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "wc binary", err)
	if strings.Contains(out, `"lines"`) {
		t.Fatalf("wc should omit lines for binary: %q", out)
	}
	if !strings.Contains(out, `"bytes":3`) {
		t.Fatalf("wc out = %q", out)
	}
}

func TestListDirTool(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("hi"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": ".", "max_depth": 1}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir", err)
	if !strings.Contains(out, `"name":"a.txt"`) || !strings.Contains(out, `"name":"subdir"`) {
		t.Fatalf("list_dir out = %q", out)
	}
	if !strings.Contains(out, `"type":"dir"`) {
		t.Fatalf("list_dir missing dir entry: %q", out)
	}
	if !strings.Contains(out, `"size":2`) {
		t.Fatalf("list_dir missing file size: %q", out)
	}
	if !strings.Contains(out, `"modified":`) {
		t.Fatalf("list_dir missing modified: %q", out)
	}
	if !strings.Contains(out, `"is_symlink":false`) {
		t.Fatalf("list_dir missing is_symlink: %q", out)
	}
}

func TestListDirToolSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "target.txt")
	testutil.FailErr(t, "write target", os.WriteFile(target, []byte("hi"), 0o644))
	testutil.FailErr(t, "symlink", os.Symlink("target.txt", filepath.Join(tmpDir, "link.txt")))
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": ".", "max_depth": 1}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir symlink", err)
	if !strings.Contains(out, `"name":"link.txt"`) || !strings.Contains(out, `"is_symlink":true`) {
		t.Fatalf("list_dir symlink entry = %q", out)
	}
	if !strings.Contains(out, `"symlink_target":"target.txt"`) {
		t.Fatalf("list_dir missing symlink_target: %q", out)
	}
}

func TestListDirToolTruncated(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":        ".",
		"max_entries": float64(2),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir truncated", err)
	if !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("list_dir out = %q", out)
	}
	if !strings.Contains(out, `"total_entries":5`) || !strings.Contains(out, `"next_offset":2`) {
		t.Fatalf("list_dir pagination fields missing: %q", out)
	}
}

func TestListDirToolOffset(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":        ".",
		"offset":      float64(2),
		"max_entries": float64(2),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir offset", err)
	if !strings.Contains(out, `"offset":2`) || !strings.Contains(out, `"total_entries":5`) {
		t.Fatalf("list_dir out = %q", out)
	}
	if !strings.Contains(out, `"next_offset":4`) {
		t.Fatalf("list_dir missing next_offset: %q", out)
	}
}

func TestStatPathEscape(t *testing.T) {
	tool := &StatTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"../etc/passwd"},
	}, nativefixture.Context(t.TempDir()))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v want SURVEY_PATH_ESCAPE", err)
	}
}

func TestWcPathEscape(t *testing.T) {
	tool := &WcTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"../etc/passwd"},
	}, nativefixture.Context(t.TempDir()))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v want SURVEY_PATH_ESCAPE", err)
	}
}

func TestListDirPathEscape(t *testing.T) {
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "../etc",
	}, nativefixture.Context(t.TempDir()))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v want SURVEY_PATH_ESCAPE", err)
	}
}
