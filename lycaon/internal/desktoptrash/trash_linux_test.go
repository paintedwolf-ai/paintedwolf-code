//go:build linux

package desktoptrash

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
)

func TestLinuxTrashMetadataAndRecovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	name := "space and\nnewline"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("kept"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := Move(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(receipt.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadata), "Path="+url.PathEscape(path)+"\n") {
		t.Fatalf("incorrect metadata: %q", metadata)
	}
	if err := Restore(t.Context(), receipt, fseffect.Location{Root: root, Rel: name}); err != nil {
		t.Fatal(err)
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
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(trash, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(trash, 0755); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "original")
			if err := os.WriteFile(path, []byte("kept"), 0600); err != nil {
				t.Fatal(err)
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
