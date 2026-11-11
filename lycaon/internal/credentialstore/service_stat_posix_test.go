//go:build !windows

package credentialstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadOrInitReportsUnexpectedVaultStatFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	testutil.FailErr(t, "make the vault path a symlink loop", os.Symlink(path, path))
	slot := Slot{Path: path, Namespace: "ns", Context: "ctx"}
	if _, err := LoadOrInit(slot, "id", nil, func() (string, error) { return "secret", nil }, nil); err == nil {
		t.Fatal("LoadOrInit ignored a vault stat failure")
	}
}
