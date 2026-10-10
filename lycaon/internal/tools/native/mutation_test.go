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

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestChmodToolSymbolicPlusX(t *testing.T) {
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	tool := &ChmodTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"run.sh"},
		"mode":  "+x",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "chmod +x", err)
	if !strings.Contains(out, `"mode_before":"644"`) || !strings.Contains(out, `"mode_after":"755"`) {
		t.Fatalf("out = %q", out)
	}
	info, err := os.Stat(script)
	testutil.FailErr(t, "stat script", err)
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected execute bit, mode=%o", info.Mode().Perm())
	}
}

func TestChmodToolOctal755(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "bin"), []byte("x"), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ChmodTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"bin"},
		"mode":  "755",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "chmod 755", err)
	if !strings.Contains(out, `"mode_after":"755"`) {
		t.Fatalf("out = %q", out)
	}
}

func TestChmodToolRejects777(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ChmodTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"a.txt"},
		"mode":  "777",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CHMOD_MODE_DENIED" {
		t.Fatalf("err = %v want CHMOD_MODE_DENIED", err)
	}
}

func TestChmodToolRejectsSetuidOctal(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ChmodTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"a.txt"},
		"mode":  "4755",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CHMOD_SPECIAL_BIT_DENIED" {
		t.Fatalf("err = %v want CHMOD_SPECIAL_BIT_DENIED", err)
	}
}

func TestChmodToolRejectsGitPath(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "config"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write config", err)
	}
	tool := &ChmodTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{".git/config"},
		"mode":  "+x",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("err = %v want GIT_INTERNALS_WRITE_DENIED", err)
	}
}

func TestChmodToolRejectsBulk(t *testing.T) {
	tmpDir := t.TempDir()
	paths := make([]any, 21)
	for i := range paths {
		paths[i] = "run.sh"
	}
	tool := &ChmodTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": paths,
		"mode":  "+x",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CHMOD_BULK_DENIED" {
		t.Fatalf("err = %v want CHMOD_BULK_DENIED", err)
	}
}

func TestChmodToolRejectsNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &ChmodTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"missing.sh"},
		"mode":  "+x",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CHMOD_NOT_FOUND" {
		t.Fatalf("err = %v want CHMOD_NOT_FOUND", err)
	}
}

func TestDeleteToolRemovesFile(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "stale.go"), []byte("package x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &DeleteTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"stale.go"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "delete file", err)
	var resp deleteResponse
	testutil.FailErr(t, "decode delete", json.Unmarshal([]byte(out), &resp))
	if len(resp.Deleted) != 1 || resp.Deleted[0] != "stale.go" {
		t.Fatalf("resp = %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "stale.go")); !os.IsNotExist(err) {
		t.Fatal("file should be removed")
	}
}

func TestDeleteToolRemovesEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, "empty"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	tool := &DeleteTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"empty"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "delete dir", err)
	if !strings.Contains(out, `"deleted":["empty"]`) {
		t.Fatalf("out = %q", out)
	}
}

func TestDeleteToolFilesOnlyRejectsEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, "empty"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	tool := &DeleteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"empty"}, "files_only": true,
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "DELETE_IS_DIRECTORY" {
		t.Fatalf("err = %v want DELETE_IS_DIRECTORY", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "empty")); statErr != nil {
		t.Fatalf("files_only removed the directory: %v", statErr)
	}
}

func TestDeleteToolRejectsNonEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "pkg")
	if err := os.Mkdir(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &DeleteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"pkg"},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "DELETE_NOT_EMPTY" {
		t.Fatalf("err = %v want DELETE_NOT_EMPTY", err)
	}
}

func TestDeleteToolRejectsBulk(t *testing.T) {
	tmpDir := t.TempDir()
	paths := make([]any, 21)
	for i := range paths {
		paths[i] = "missing.txt"
	}
	tool := &DeleteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"paths": paths}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "DELETE_BULK_DENIED" {
		t.Fatalf("err = %v want DELETE_BULK_DENIED", err)
	}
}

func TestDeleteToolRejectsNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &DeleteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"missing.go"},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "DELETE_NOT_FOUND" {
		t.Fatalf("err = %v want DELETE_NOT_FOUND", err)
	}
}

func TestDeleteToolRejectsGitPath(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write HEAD", err)
	}
	tool := &DeleteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{".git/HEAD"},
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" || reject.Data["class"] != "metadata" {
		t.Fatalf("err = %v want GIT_INTERNALS_WRITE_DENIED for repository metadata", err)
	}
}

func TestChmodRecoveryModesMatchAcceptedPermissions(t *testing.T) {
	reject := toolrejection.AsToolReject(chmodModeDenied("777"))
	modes, ok := reject.Data["chmod_allowed_modes"].([]string)
	if !ok || len(modes) == 0 {
		t.Fatalf("missing recoverable modes: %+v", reject.Data)
	}
	offered := map[os.FileMode]bool{}
	for _, mode := range modes {
		resolved, err := resolveChmodMode(0o600, mode)
		testutil.FailErr(t, "resolve advertised mode", err)
		offered[resolved] = true
	}
	for mode := os.FileMode(0); mode <= 0o777; mode++ {
		err := validateChmodResult(mode)
		if (err == nil) != offered[mode] {
			t.Fatalf("mode %o: accepted=%v advertised=%v", mode, err == nil, offered[mode])
		}
	}
}

func TestOctalPermissionParsingRejectsPartialInput(t *testing.T) {
	for _, spec := range []string{"755x", "00755", "755\n", "755 ", "x755", "1755", "4755", "8755", "7559", "0755junk"} {
		if _, err := parseOctalChmodMode(spec); err == nil {
			t.Errorf("accepted malformed or special mode %q", spec)
		}
	}
	for _, spec := range []string{"755", "0755", "644", "0644"} {
		_, err := parseOctalChmodMode(spec)
		testutil.FailErr(t, "parse complete octal mode", err)
	}
}
