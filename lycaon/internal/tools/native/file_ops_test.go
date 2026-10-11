package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestCopyToolCopiesFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "fixtures", "a.json")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		testutil.FailErr(t, "mkdir fixtures", err)
	}
	if err := os.WriteFile(src, []byte(`{"ok":true}`), 0o644); err != nil {
		testutil.FailErr(t, "write src", err)
	}
	tool := &CopyTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"copies": []any{
			map[string]any{"from": "fixtures/a.json", "to": "internal/testdata/a.json"},
		},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "copy file", err)
	dst := filepath.Join(tmpDir, "internal", "testdata", "a.json")
	data, err := os.ReadFile(dst)
	testutil.FailErr(t, "read dst", err)
	if string(data) != `{"ok":true}` {
		t.Fatalf("dst content = %q", data)
	}
	if !strings.Contains(out, `"bytes":`) {
		t.Fatalf("out = %q", out)
	}
}

func TestCopyToolRequiresCanonicalPairFields(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "src.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write src", err)
	}
	tool := &CopyTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"copies": []any{
			map[string]any{"source": "src.txt", "destination": "dst.txt"},
		},
	}, nativefixture.Context(tmpDir))
	if err == nil || !strings.Contains(err.Error(), "need from and to") {
		t.Fatalf("alternate fields error = %v, want canonical field requirement", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "dst.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("alternate fields created destination: %v", statErr)
	}
}

func TestCopyToolRejectsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, "pkg"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	tool := &CopyTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"copies": []any{
			map[string]any{"from": "pkg", "to": "pkg-copy"},
		},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COPY_IS_DIRECTORY" {
		t.Fatalf("err = %v want COPY_IS_DIRECTORY", err)
	}
}

func TestCopyToolRejectsSizeExceeded(t *testing.T) {
	tmpDir := t.TempDir()
	payload := strings.Repeat("x", 100)
	if err := os.WriteFile(filepath.Join(tmpDir, "big.txt"), []byte(payload), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CopyTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"copies": []any{
			map[string]any{"from": "big.txt", "to": "copy.txt"},
		},
		"max_file_bytes": 50,
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COPY_SIZE_EXCEEDED" {
		t.Fatalf("err = %v want COPY_SIZE_EXCEEDED", err)
	}
}

func TestCopyToolRejectsBulk(t *testing.T) {
	tmpDir := t.TempDir()
	copies := make([]any, 11)
	for i := range copies {
		copies[i] = map[string]any{"from": "a.txt", "to": "b.txt"}
	}
	tool := &CopyTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"copies": copies}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COPY_BULK_DENIED" {
		t.Fatalf("err = %v want COPY_BULK_DENIED", err)
	}
}

func TestCopyToolRejectsGitPath(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "config"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write config", err)
	}
	tool := &CopyTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"copies": []any{
			map[string]any{"from": ".git/config", "to": "config-copy"},
		},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("err = %v want GIT_INTERNALS_WRITE_DENIED", err)
	}
}

func TestMoveToolRenamesFile(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "old.go"), []byte("package old"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &MoveTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"moves": []any{
			map[string]any{"from": "old.go", "to": "newpkg/new.go"},
		},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "move file", err)
	if !strings.Contains(out, `"moved"`) {
		t.Fatalf("out = %q", out)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "newpkg", "new.go")); err != nil {
		t.Fatal("expected new file")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "old.go")); !os.IsNotExist(err) {
		t.Fatal("old file should be gone")
	}
}

func TestMoveToolOverwritesDestination(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "src.txt"), []byte("new"), 0o644); err != nil {
		testutil.FailErr(t, "write src", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "dst.txt"), []byte("old"), 0o644); err != nil {
		testutil.FailErr(t, "write dst", err)
	}
	tool := &MoveTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"moves": []any{
			map[string]any{"from": "src.txt", "to": "dst.txt"},
		},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "move overwrite", err)
	data, err := os.ReadFile(filepath.Join(tmpDir, "dst.txt"))
	testutil.FailErr(t, "read dst", err)
	if string(data) != "new" {
		t.Fatalf("dst = %q", data)
	}
}

func TestMoveToolRejectsNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &MoveTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"moves": []any{
			map[string]any{"from": "missing.go", "to": "new.go"},
		},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "MOVE_NOT_FOUND" {
		t.Fatalf("err = %v want MOVE_NOT_FOUND", err)
	}
}

func TestMoveToolRejectsCrossRootRename(t *testing.T) {
	primary := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, "old.go"), []byte("package old"), 0o644); err != nil {
		testutil.FailErr(t, "write source", err)
	}
	tctx := nativefixture.Context(primary)
	tctx.Source.Roots = append(tctx.Source.Roots, projectroot.RootRef{ID: "r2", Label: "other", Path: other})
	tool := &MoveTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"moves": []any{map[string]any{"from": "old.go", "to": "@other/new.go"}},
	}, tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "MOVE_CROSS_ROOT" {
		t.Fatalf("err = %v want MOVE_CROSS_ROOT", err)
	}
	if _, statErr := os.Stat(filepath.Join(primary, "old.go")); statErr != nil {
		testutil.FailErr(t, "source changed after rejection", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(other, "new.go")); !os.IsNotExist(statErr) {
		t.Fatalf("destination exists after rejection: %v", statErr)
	}
}

func TestMkdirToolCreatesPath(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &MkdirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"internal/newpkg/sub"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "mkdir", err)
	var resp mkdirResponse
	testutil.FailErr(t, "decode mkdir", json.Unmarshal([]byte(out), &resp))
	if len(resp.Created) != 1 || resp.Created[0] != "internal/newpkg/sub" {
		t.Fatalf("resp = %+v", resp)
	}
	info, err := os.Stat(filepath.Join(tmpDir, "internal", "newpkg", "sub"))
	testutil.FailErr(t, "stat dir", err)
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}

func TestMkdirToolDefaultMode755(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &MkdirTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"bin"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "mkdir", err)
	info, err := os.Stat(filepath.Join(tmpDir, "bin"))
	testutil.FailErr(t, "stat dir", err)
	if info.Mode().Perm()&0o755 != 0o755 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestMkdirToolRejectsFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &MkdirTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"file.txt"},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "MKDIR_FILE_EXISTS" {
		t.Fatalf("err = %v want MKDIR_FILE_EXISTS", err)
	}
}

func TestMkdirToolRejects777(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &MkdirTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"bad"},
		"mode":  "777",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "MKDIR_MODE_DENIED" {
		t.Fatalf("err = %v want MKDIR_MODE_DENIED", err)
	}
}

func TestWriteToolCreatesMissingParentDirs(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":    "src/newpkg/deep/nested/file.go",
		"content": "package nested\n",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "write into missing directories", err)
	data, err := os.ReadFile(filepath.Join(tmpDir, "src", "newpkg", "deep", "nested", "file.go"))
	testutil.FailErr(t, "read created file", err)
	if string(data) != "package nested\n" {
		t.Fatalf("content = %q", data)
	}
	if !strings.Contains(out, "file.go") {
		t.Fatalf("out = %q", out)
	}
}
