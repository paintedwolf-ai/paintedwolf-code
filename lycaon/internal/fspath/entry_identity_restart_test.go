package fspath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSameEntryAcrossRestartIgnoresOnlyTheDevice(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("same bytes"), 0o600); err != nil {
			t.Fatal("seed: ", err)
		}
	}
	firstID, err := EntryIdentity(first)
	if err != nil {
		t.Skipf("platform has no entry identity: %v", err)
	}
	secondID, err := EntryIdentity(second)
	if err != nil {
		t.Fatal("identify second: ", err)
	}
	moved := "99" + firstID
	if !SameEntryAcrossRestart(moved, firstID) || !SameEntryAcrossRestart(firstID, firstID) {
		t.Fatalf("device change refused: %q vs %q", moved, firstID)
	}
	if SameEntryAcrossRestart(moved, secondID) || SameEntryAcrossRestart(firstID, secondID) {
		t.Fatal("a different entry matched")
	}
	if SameEntryAcrossRestart("", firstID) || SameEntryAcrossRestart(firstID, "") {
		t.Fatal("an empty identity matched")
	}
	inode := strings.Split(firstID, ":")
	for _, recorded := range []string{"1:" + inode[len(inode)-1], "2:" + inode[len(inode)-1]} {
		if !SameEntryAcrossRestart(recorded, "7:"+inode[len(inode)-1]) {
			t.Fatalf("two-field identity %q refused", recorded)
		}
	}
}
