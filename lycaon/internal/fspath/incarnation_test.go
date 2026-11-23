package fspath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIncarnationRequiresCreationDiscriminator(t *testing.T) {
	t.Parallel()
	for _, bare := range []string{"", "16777220:99123", "259:2:99123", "16777220:99123:0:0", "259:2:99123:0:0", "16777220:99123:h:"} {
		if HasCreationDiscriminator(bare) {
			t.Fatalf("HasCreationDiscriminator(%q) = true, want false", bare)
		}
		if SameIncarnation(bare, bare) {
			t.Fatalf("SameIncarnation(%q, %q) = true, want false", bare, bare)
		}
	}

	if !HasCreationDiscriminator("16777220:99123:1700000000:12345") {
		t.Fatal("darwin birth-time identity rejected")
	}
	if !HasCreationDiscriminator("259:2:99123:1700000000:0") {
		t.Fatal("linux statx birth-time identity rejected")
	}
	if !HasCreationDiscriminator("64769:99123:h1:01020304") {
		t.Fatal("linux file-handle identity rejected")
	}
	if !SameIncarnation("16777220:99123:1700000000:12345", "16777299:99123:1700000000:12345") {
		t.Fatal("remounted device number prevented incarnation match")
	}
	if SameIncarnation("16777220:99123:1700000000:12345", "16777220:99123:1700000001:12345") {
		t.Fatal("recreated entry with reused inode matched prior incarnation")
	}
}

func TestIncarnationDetectsDirectoryRecreationAtSamePath(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "checkout")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir first: %v", err)
	}
	first, err := EntryIdentity(dir)
	if err != nil {
		t.Fatalf("identity first: %v", err)
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatalf("rename: %v", err)
	}
	afterMove, err := EntryIdentity(moved)
	if err != nil {
		t.Fatalf("identity moved: %v", err)
	}
	if HasCreationDiscriminator(first) && !SameIncarnation(first, afterMove) {
		t.Fatalf("rename lost incarnation continuity: %q -> %q", first, afterMove)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir replacement: %v", err)
	}
	replacement, err := EntryIdentity(dir)
	if err != nil {
		t.Fatalf("identity replacement: %v", err)
	}
	if SameIncarnation(first, replacement) {
		t.Fatalf("replacement directory matched first incarnation: %q vs %q", first, replacement)
	}
}
