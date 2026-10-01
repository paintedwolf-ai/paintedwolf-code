package blobcache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvictRespectsKeep(t *testing.T) {
	dir := t.TempDir()
	keepHash := strings.Repeat("a", 64)
	dropHash := strings.Repeat("b", 64)
	if err := WriteFile(dir, keepHash+".bin", make([]byte, 600), 1000, nil); err != nil {
		testutil.FailErr(t, "write retained entry", err)
	}
	if err := WriteFile(dir, dropHash+".bin", make([]byte, 600), 1000, func(name string) bool {
		return strings.HasPrefix(name, keepHash)
	}); err != nil {
		testutil.FailErr(t, "write evicted entry", err)
	}
	if _, err := os.Stat(filepath.Join(dir, keepHash+".bin")); err != nil {
		t.Fatal("kept blob was pruned")
	}
}

func TestReplaceEntryMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.bin")
	if err := replaceEntry(dir, "x.bin", []byte("abc")); err != nil {
		testutil.FailErr(t, "replaceEntry failed", err)
	}
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat path", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v want 0600", info.Mode().Perm())
	}
}
