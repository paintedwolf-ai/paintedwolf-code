package desktoptrash

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveValidatesAbsolutePath(t *testing.T) {
	ctx := context.Background()
	if _, err := Move(ctx, "relative/path"); err == nil {
		t.Fatal("expected error for relative path, got nil")
	}
	if _, err := Move(ctx, "/path/with\x00null"); err == nil {
		t.Fatal("expected error for path with null character, got nil")
	}
}

func TestMoveFileToTrash(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test_file.txt")
	if err := os.WriteFile(filePath, []byte("test content"), 0o600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	ctx := context.Background()
	if _, err := Move(ctx, filePath); err != nil {
		t.Fatalf("Move failed: %v", err)
	}

	if _, err := os.Lstat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be moved to trash, but it still exists: %v", err)
	}
}

func TestMoveDirectoryToTrash(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "test_dir")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}
	subFile := filepath.Join(subDir, "inner.txt")
	if err := os.WriteFile(subFile, []byte("inner"), 0o600); err != nil {
		t.Fatalf("failed to create inner file: %v", err)
	}

	ctx := context.Background()
	if _, err := Move(ctx, subDir); err != nil {
		t.Fatalf("Move failed on directory: %v", err)
	}

	if _, err := os.Lstat(subDir); !os.IsNotExist(err) {
		t.Fatalf("expected directory to be moved to trash, but it still exists: %v", err)
	}
}
