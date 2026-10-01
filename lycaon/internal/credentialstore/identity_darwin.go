package credentialstore

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/version"
)

const keychainService = version.BundleID + ".credential-vault"

type keychainItems interface {
	read(account string) ([]byte, error)
	create(account string, value []byte) error
	remove(account string) error
}

type keychainIdentityProvider struct {
	account string
	items   keychainItems
}

func newKeychainIdentityProvider(path string) identityProvider {
	// Configuration roots have separate identity accounts.
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return &keychainIdentityProvider{
		account: fmt.Sprintf("age-x25519-identity-v1:%x", digest), items: dataProtectionItems{},
	}
}

func (*keychainIdentityProvider) Describe() string {
	return "device-only identity in macOS data protection Keychain"
}

func (p *keychainIdentityProvider) LoadOrCreate(vaultExists bool) (identityDocument, error) {
	raw, err := p.items.read(p.account)
	if err == nil {
		defer clear(raw)
		return decodeIdentityDocument(raw)
	}
	if !errors.Is(err, keychainNotFound) {
		return identityDocument{}, fmt.Errorf("read credential vault identity: %w", err)
	}
	if vaultExists {
		return identityDocument{}, fmt.Errorf("%w: macOS data protection Keychain identity is missing", ErrVaultCorrupt)
	}
	doc, err := generateIdentityDocument()
	if err != nil {
		return identityDocument{}, err
	}
	raw, err = encodeIdentityDocument(doc)
	if err != nil {
		return identityDocument{}, err
	}
	defer clear(raw)
	if err := p.items.create(p.account, raw); err != nil {
		if errors.Is(err, keychainDuplicate) {
			// Concurrent creation uses the stored identity.
			return p.LoadOrCreate(true)
		}
		return identityDocument{}, fmt.Errorf("store credential vault identity: %w", err)
	}
	// Readback validates the stored identity and its protection attributes.
	return p.LoadOrCreate(true)
}

func (p *keychainIdentityProvider) Purge() error {
	err := p.items.remove(p.account)
	if err != nil && !errors.Is(err, keychainNotFound) {
		return fmt.Errorf("delete credential vault identity: %w", err)
	}
	return nil
}
