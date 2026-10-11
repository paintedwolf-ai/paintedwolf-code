//go:build linux

package desktoptrash

import (
	"context"
	"errors"
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
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Move(canceled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled move = %v, want cancellation", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "kept" {
		t.Fatalf("canceled move changed source: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Trash")); !os.IsNotExist(err) {
		t.Fatalf("canceled move created Trash: %v", err)
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
	destination := fseffect.Location{Root: root, Rel: name}
	if err := Restore(canceled, receipt, destination); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restoration = %v, want cancellation", err)
	}
	tampered := receipt
	tampered.Identity += "-different-entry"
	if err := Restore(t.Context(), tampered, destination); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("changed receipt identity = %v, want unavailable", err)
	}
	testutil.FailErr(t, "occupy restoration destination", os.WriteFile(path, []byte("new entry"), 0600))
	if err := Restore(t.Context(), receipt, destination); err == nil {
		t.Fatal("restoration overwrote an occupied destination")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "new entry" {
		t.Fatalf("refused restoration changed destination: %q, %v", body, err)
	}
	if body, err := os.ReadFile(receipt.Path); err != nil || string(body) != "kept" {
		t.Fatalf("refused restorations changed Trash entry: %q, %v", body, err)
	}
	if info, err := os.ReadFile(receipt.Metadata); err != nil || string(info) != string(metadata) {
		t.Fatalf("refused restorations changed recovery metadata: %q, %v", info, err)
	}
	testutil.FailErr(t, "release restoration destination", os.Remove(path))
	if err := Restore(t.Context(), receipt, destination); err != nil {
		testutil.FailErr(t, "restore native Trash receipt", err)
	}
	if _, err := os.Stat(receipt.Metadata); !os.IsNotExist(err) {
		t.Fatalf("native metadata remains: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "kept" {
		t.Fatalf("restored contents = %q, %v", body, err)
	}
	if err := Restore(t.Context(), receipt, destination); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("consumed receipt = %v, want unavailable", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "kept" {
		t.Fatalf("receipt replay changed restored contents: %q, %v", body, err)
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
	t.Setenv("XDG_DATA_HOME", "relative-data-home")
	t.Setenv("HOME", root)
	name := strings.Repeat("x", 255)
	path := filepath.Join(root, name)
	testutil.FailErr(t, "create maximum length file", os.WriteFile(path, []byte("kept"), 0600))
	receipt, err := Move(t.Context(), path)
	testutil.FailErr(t, "trash maximum length file", err)
	if filepath.Dir(receipt.Path) != filepath.Join(root, ".local", "share", "Trash", "files") {
		t.Fatalf("relative XDG data home did not fall back to HOME: %q", receipt.Path)
	}
	testutil.FailErr(t, "restore original name", Restore(t.Context(), receipt, fseffect.Location{Root: root, Rel: name}))
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored file", err)
	if string(body) != "kept" {
		t.Fatal("recovery lost contents")
	}
}
