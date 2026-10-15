package credentialstore

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
)

var (
	// errIdentityMissing: the custodian holds no identity for the account.
	errIdentityMissing = errors.New("identity is missing")
	// errIdentityExists: a concurrent creator stored an identity first.
	errIdentityExists = errors.New("identity exists")
)

// identityItems is a custodian that keeps identities beyond this process's
// reach: the macOS data protection Keychain or the Linux identity service.
type identityItems interface {
	read(account string) ([]byte, error)
	create(account string, value []byte) error
	remove(account string) error
}

// itemsIdentityProvider keeps one vault's identity with a custodian under an
// account bound to the configuration root.
type itemsIdentityProvider struct {
	account     string
	description string
	items       identityItems
}

func identityAccount(vaultPath string) string {
	// Configuration roots have separate identity accounts.
	digest := sha256.Sum256([]byte(filepath.Clean(vaultPath)))
	return fmt.Sprintf("age-x25519-identity-v1:%x", digest)
}

func (p *itemsIdentityProvider) Describe() string { return p.description }

func (p *itemsIdentityProvider) LoadOrCreate(vaultExists bool) (identityDocument, error) {
	raw, err := p.items.read(p.account)
	if err == nil {
		defer clear(raw)
		return decodeIdentityDocument(raw)
	}
	if !errors.Is(err, errIdentityMissing) {
		return identityDocument{}, fmt.Errorf("read credential vault identity: %w", err)
	}
	if vaultExists {
		return identityDocument{}, fmt.Errorf("%w: the %s is missing", ErrVaultCorrupt, p.description)
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
		if errors.Is(err, errIdentityExists) {
			// Concurrent creation uses the stored identity.
			return p.LoadOrCreate(true)
		}
		return identityDocument{}, fmt.Errorf("store credential vault identity: %w", err)
	}
	// Readback validates the stored identity and its protection attributes.
	return p.LoadOrCreate(true)
}

func (p *itemsIdentityProvider) Purge() error {
	err := p.items.remove(p.account)
	if err != nil && !errors.Is(err, errIdentityMissing) {
		return fmt.Errorf("delete credential vault identity: %w", err)
	}
	return nil
}

// verifyDisposableIdentities proves the custodian keeps accounts apart and
// forgets what it deletes, using two throwaway providers.
func verifyDisposableIdentities(a, b *itemsIdentityProvider) error {
	first, err := a.LoadOrCreate(false)
	if err != nil {
		return err
	}
	second, err := b.LoadOrCreate(false)
	if err != nil {
		return err
	}
	if first == second {
		return errors.New("isolated identities are equal")
	}
	if err := a.Purge(); err != nil {
		return err
	}
	if _, err := a.items.read(a.account); err == nil {
		return errors.New("deleted identity remains accessible")
	} else if !errors.Is(err, errIdentityMissing) {
		return fmt.Errorf("verify deleted identity: %w", err)
	}
	loaded, err := b.LoadOrCreate(true)
	if err != nil {
		return err
	}
	if loaded != second {
		return errors.New("deleting another identity changed the retained identity")
	}
	return nil
}
