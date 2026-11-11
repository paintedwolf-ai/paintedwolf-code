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
	if sharedVault(path, refused) == sharedVault(path, refused) {
		t.Fatal("refused custody shared a vault instance")
	}
}
