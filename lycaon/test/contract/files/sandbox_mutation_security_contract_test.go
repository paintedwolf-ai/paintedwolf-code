package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestChmod777RejectedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	chmod := &native.ChmodTool{Boundary: b}
	_, err := chmod.Run(context.Background(), map[string]any{
		"paths": []any{"run.sh"},
		"mode":  "777",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected chmod 777 rejection")
	}
}

func TestChmodPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	chmod := &native.ChmodTool{Boundary: b}
	_, err := chmod.Run(context.Background(), map[string]any{
		"paths": []any{"../outside.sh"},
		"mode":  "+x",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestDeletePathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	del := &native.DeleteTool{Boundary: b}
	_, err := del.Run(context.Background(), map[string]any{
		"paths": []any{"../outside.txt"},
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestDeleteGitPathDeniedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write HEAD", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	del := &native.DeleteTool{Boundary: b}
	_, err := del.Run(context.Background(), map[string]any{
		"paths": []any{".git/HEAD"},
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected delete path denied")
	}
}

func TestCopyPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	copyTool := &native.CopyTool{Boundary: b}
	_, err := copyTool.Run(context.Background(), map[string]any{
		"copies": []any{
			map[string]any{"from": "../outside.txt", "to": "copy.txt"},
		},
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestCopySizeCapBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.bin"), []byte(strings.Repeat("a", 128)), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	copyTool := &native.CopyTool{Boundary: b}
	_, err := copyTool.Run(context.Background(), map[string]any{
		"copies": []any{
			map[string]any{"from": "big.bin", "to": "copy.bin"},
		},
		"max_file_bytes": 64,
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected copy size cap rejection")
	}
}

func TestMovePathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	moveTool := &native.MoveTool{Boundary: b}
	_, err := moveTool.Run(context.Background(), map[string]any{
		"moves": []any{
			map[string]any{"from": "../outside.txt", "to": "moved.txt"},
		},
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestMoveOverwriteSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("new"), 0o644); err != nil {
		testutil.FailErr(t, "write src", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dst.txt"), []byte("old"), 0o644); err != nil {
		testutil.FailErr(t, "write dst", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	moveTool := &native.MoveTool{Boundary: b}
	_, err := moveTool.Run(context.Background(), map[string]any{
		"moves": []any{
			map[string]any{"from": "src.txt", "to": "dst.txt"},
		},
	}, implementToolContext(root))
	if err != nil {
		t.Fatalf("move overwrite: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "dst.txt"))
	testutil.FailErr(t, "read dst", err)
	if string(data) != "new" {
		t.Fatalf("dst = %q", data)
	}
}

func TestMkdirPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	mkdirTool := &native.MkdirTool{Boundary: b}
	_, err := mkdirTool.Run(context.Background(), map[string]any{
		"paths": []any{"../outside"},
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestChownPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	chownTool := &native.ChownTool{Boundary: b}
	_, err := chownTool.Run(context.Background(), map[string]any{
		"paths": []any{"../outside.sh"},
		"owner": "current",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestChownForeignUIDBlockedSecurity(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 65534 {
		t.Skip("effective uid is nobody")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	chownTool := &native.ChownTool{Boundary: b}
	_, err := chownTool.Run(context.Background(), map[string]any{
		"paths": []any{"a.txt"},
		"owner": "65534",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected foreign uid rejection")
	}
}

func TestChownGitPathDeniedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write HEAD", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	chownTool := &native.ChownTool{Boundary: b}
	_, err := chownTool.Run(context.Background(), map[string]any{
		"paths": []any{".git/HEAD"},
		"owner": "current",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected chown path denied")
	}
}
