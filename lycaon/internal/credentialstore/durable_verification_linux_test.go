//go:build linux

package credentialstore

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRefusedCustodyNeverSharesVaultInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	refused := refusedIdentityProvider{refusal: errors.New("launcher refused")}
	first := sharedVault(path, refused)
	second := sharedVault(path, refused)
	if first == second {
		t.Fatal("refused custody shared a vault instance")
	}
}
