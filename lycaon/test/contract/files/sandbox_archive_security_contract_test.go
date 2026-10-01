package contract

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func nativeWriteTestZipFile(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		testutil.FailErr(t, "write zip", err)
	}
}

func TestExtractArchiveZipSlipBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	zipPath := filepath.Join(root, "evil.zip")
	nativeWriteTestZipFile(t, zipPath, map[string]string{"../outside.txt": "x"})
	b := contractcheck.ProdToolBoundary(t)
	tool := &native.ExtractArchiveTool{Boundary: b}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "evil.zip",
		"dest": "out",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected zip slip rejection")
	}
}

func TestExtractArchiveSizeCapBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	zipPath := filepath.Join(root, "bomb.zip")
	nativeWriteTestZipFile(t, zipPath, map[string]string{
		"huge.bin": strings.Repeat("z", (50<<20)+1024),
	})
	b := contractcheck.ProdToolBoundary(t)
	tool := &native.ExtractArchiveTool{Boundary: b}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "bomb.zip",
		"dest": "out",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected size cap rejection")
	}
}

func TestExtractArchiveDestEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	zipPath := filepath.Join(root, "ok.zip")
	nativeWriteTestZipFile(t, zipPath, map[string]string{"a.txt": "ok"})
	b := contractcheck.ProdToolBoundary(t)
	tool := &native.ExtractArchiveTool{Boundary: b}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "ok.zip",
		"dest": "../outside",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected dest path escape error")
	}
}
