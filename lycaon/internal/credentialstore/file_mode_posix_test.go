//go:build !windows

package credentialstore

import (
	"os"
	"testing"
)

// assertOwnerOnly checks exact owner-only permissions.
func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if got := info.Mode().Perm(); got != fileMode {
		t.Fatalf("credentials mode = %o, want %o", got, fileMode)
	}
}
