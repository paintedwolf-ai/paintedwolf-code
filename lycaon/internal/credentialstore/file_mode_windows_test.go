//go:build windows

package credentialstore

import (
	"os"
	"testing"
)

// assertOwnerOnly checks the writable bit exposed through FileMode.
func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("credentials file is marked read-only (mode %o); updates would fail", info.Mode().Perm())
	}
}
