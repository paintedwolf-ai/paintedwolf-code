package desktoptrash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
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
	receipt, err := Move(ctx, filePath)
	if err != nil {
		t.Fatalf("Move failed: %v", err)
	}

	if _, err := os.Lstat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be moved to trash, but it still exists: %v", err)
	}
	if err := Restore(ctx, receipt, fseffect.Location{Root: tmpDir, Rel: filepath.Base(filePath)}); err != nil {
		t.Fatalf("restore native receipt: %v", err)
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
	receipt, err := Move(ctx, subDir)
	if err != nil {
		t.Fatalf("Move failed on directory: %v", err)
	}

	if _, err := os.Lstat(subDir); !os.IsNotExist(err) {
		t.Fatalf("expected directory to be moved to trash, but it still exists: %v", err)
	}
	if err := Restore(ctx, receipt, fseffect.Location{Root: tmpDir, Rel: filepath.Base(subDir)}); err != nil {
		t.Fatalf("restore native directory receipt: %v", err)
	}
}

func TestRestoreRefusesUnknownReceiptVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "recorded")
	testutil.FailErr(t, "write retained receipt fixture", os.WriteFile(path, []byte("preserved"), 0600))
	err := Restore(t.Context(), Receipt{FormatVersion: 999, Platform: runtime.GOOS, Path: path, Identity: "unknown"}, fseffect.Location{Root: root, Rel: "destination"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown receipt = %v", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "preserved" {
		t.Fatalf("recorded item changed: %q %v", body, err)
	}
}
