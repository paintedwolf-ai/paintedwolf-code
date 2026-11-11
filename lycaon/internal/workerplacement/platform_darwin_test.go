//go:build darwin

package workerplacement

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func assertExcluded(t *testing.T, dir string) {
	t.Helper()
	value := make([]byte, 128)
	n, err := unix.Getxattr(dir, backupExcludeAttribute, value)
	if err != nil || !bytes.Equal(value[:n], backupExcludeValue) {
		t.Fatalf("backup attribute = %q, %v", value[:n], err)
	}
	if out, err := exec.Command("tmutil", "isexcluded", dir).CombinedOutput(); err == nil && !strings.Contains(string(out), "[Excluded]") {
		t.Fatalf("tmutil does not see the exclusion: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".metadata_never_index")); err != nil {
		t.Fatalf("index marker: %v", err)
	}
}
