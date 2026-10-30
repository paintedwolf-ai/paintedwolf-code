//go:build linux

package hitl_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Linux names are raw bytes, so a real directory entry whose name is not
// UTF-8 keeps its exact identity through durable approval authority.
func TestNativeSocketAuthorityKeepsLinuxRawByteEntryIdentity(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "create raw-byte entry", os.WriteFile(filepath.Join(dir, "socket\xff\xfe "), nil, 0o600))
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "list raw-byte entry", err)
	if len(entries) != 1 || !strings.Contains(entries[0].Name(), "\xff\xfe") {
		t.Fatalf("filesystem entry = %v", entries)
	}
	path := filepath.Join(dir, entries[0].Name())
	assertNativeSocketRoundTrip(t, path)
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("restored authority no longer names the entry: %v", err)
	}
}
