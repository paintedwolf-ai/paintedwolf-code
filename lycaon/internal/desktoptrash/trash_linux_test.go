//go:build linux

package desktoptrash

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxTrashMetadataAndRecovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	name := "space and\nnewline"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("kept"), 0600); err != nil {
		testutil.FailErr(t, "seed native Trash file", err)
	}
	receipt, err := Move(t.Context(), path)
	if err != nil {
		testutil.FailErr(t, "move file to native Trash", err)
	}
	metadata, err := os.ReadFile(receipt.Metadata)
	if err != nil {
		testutil.FailErr(t, "read native Trash metadata", err)
	}
	if !strings.Contains(string(metadata), "Path="+url.PathEscape(path)+"\n") {
		t.Fatalf("incorrect metadata: %q", metadata)
	}
	if err := Restore(t.Context(), receipt, fseffect.Location{Root: root, Rel: name}); err != nil {
		testutil.FailErr(t, "restore native Trash receipt", err)
	}
	if _, err := os.Stat(receipt.Metadata); !os.IsNotExist(err) {
		t.Fatalf("native metadata remains: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "kept" {
		t.Fatalf("restored contents = %q, %v", body, err)
	}
}

func TestLinuxTrashRejectsUnsafeDirectories(t *testing.T) {
	for _, mode := range []string{"symlink", "public"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_DATA_HOME", root)
			trash := filepath.Join(root, "Trash")
			if mode == "symlink" {
				if err := os.Symlink(t.TempDir(), trash); err != nil {
					testutil.FailErr(t, "create unsafe symlink fixture", err)
				}
			} else {
				if err := os.Mkdir(trash, 0700); err != nil {
					testutil.FailErr(t, "create Trash fixture", err)
				}
				if err := os.Chmod(trash, 0755); err != nil {
					testutil.FailErr(t, "make Trash fixture public", err)
				}
			}
			path := filepath.Join(root, "original")
			if err := os.WriteFile(path, []byte("kept"), 0600); err != nil {
				testutil.FailErr(t, "seed original file", err)
			}
			if _, err := Move(t.Context(), path); err == nil {
				t.Fatal("unsafe Trash accepted")
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "kept" {
				t.Fatalf("source changed: %q, %v", body, err)
			}
		})
	}
}

func TestLinuxTrashPreservesMaximumLengthName(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	name := strings.Repeat("x", 255)
	path := filepath.Join(root, name)
	testutil.FailErr(t, "create maximum length file", os.WriteFile(path, []byte("kept"), 0600))
	receipt, err := Move(t.Context(), path)
	testutil.FailErr(t, "trash maximum length file", err)
	testutil.FailErr(t, "restore original name", Restore(t.Context(), receipt, fseffect.Location{Root: root, Rel: name}))
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored file", err)
	if string(body) != "kept" {
		t.Fatal("recovery lost contents")
	}
}
