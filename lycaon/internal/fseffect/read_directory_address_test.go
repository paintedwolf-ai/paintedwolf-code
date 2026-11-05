//go:build darwin || linux

package fseffect

import (
	"os"
	"path/filepath"
	"testing"
)

func openNamedDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}

func TestDirectoryAddressRereadsWithoutAllocatingUntilItMoves(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "first")
	dir := openNamedDirectory(t, first)
	known, err := readDirectoryPath(dir, "")
	if err != nil || filepath.Base(known) != "first" {
		t.Fatalf("address = %q, err = %v", known, err)
	}
	allocations := testing.AllocsPerRun(100, func() {
		if address, readErr := readDirectoryPath(dir, known); readErr != nil || address != known {
			t.Fatalf("address = %q, err = %v", address, readErr)
		}
	})
	if allocations != 0 {
		t.Fatalf("allocations = %v", allocations)
	}
	if err := os.Rename(first, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	current, err := readDirectoryPath(dir, known)
	if err != nil || filepath.Base(current) != "moved" {
		t.Fatalf("address = %q, err = %v", current, err)
	}
}

func TestDirectoryPathReportsAMoveAfterAnEarlierRead(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenReadRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	first := filepath.Join(base, "first")
	dir := openNamedDirectory(t, first)
	if path, err := root.DirectoryPath(dir); err != nil || path != first {
		t.Fatalf("path = %q, err = %v", path, err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(first, moved); err != nil {
		t.Fatal(err)
	}
	if path, err := root.DirectoryPath(dir); err != nil || path != moved {
		t.Fatalf("path = %q, err = %v", path, err)
	}
}
