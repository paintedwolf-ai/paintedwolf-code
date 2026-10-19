package credentialstore

import (
	"errors"
	"fmt"
	"os"
)

// serviceHostProvider opens one vault on a host whose identity service
// verified this engine. The service's identity wins; a vault the service
// does not hold stays on the app password while its wrapped identity
// exists, until the person moves it.
type serviceHostProvider struct {
	service *itemsIdentityProvider
	wrapped *passphraseIdentityProvider
}

func (c custody) serviceHost(path string) *serviceHostProvider {
	return &serviceHostProvider{
		service: &itemsIdentityProvider{
			account: identityAccount(path), description: "identity held by the Painted Wolf Code vault identity service",
			items: serviceItems{client: c.client},
		},
		wrapped: &passphraseIdentityProvider{path: identityPath(path)},
	}
}

func (p *serviceHostProvider) resolve() (identityProvider, error) {
	held, err := p.service.holds()
	if err != nil {
		return nil, fmt.Errorf("read credential vault identity: %w", err)
	}
	if held {
		return p.service, nil
	}
	if _, err := os.Lstat(p.wrapped.path); err == nil {
		return p.wrapped, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read wrapped credential vault identity: %w", err)
	}
	return p.service, nil
}

func (p *serviceHostProvider) Describe() string {
	active, err := p.resolve()
	if err != nil {
		return p.service.Describe()
	}
	return active.Describe()
}

func (p *serviceHostProvider) LoadOrCreate(vaultExists bool) (identityDocument, error) {
	active, err := p.resolve()
	if err != nil {
		return identityDocument{}, err
	}
	return active.LoadOrCreate(vaultExists)
}

func (p *serviceHostProvider) Purge() error {
	return errors.Join(p.service.Purge(), p.wrapped.Purge())
}

func describeCustody(path string) (CustodyReport, error) { return linuxCustody().report(path) }

func moveToService(path string) error { return linuxCustody().moveToService(path) }

func moveToAppPassword(path string) error { return linuxCustody().moveToAppPassword(path) }

func (c custody) report(path string) (CustodyReport, error) {
	switch c.mode {
	case custodyRefused:
		return CustodyReport{Mode: CustodyRefused, Detail: c.refusal.Error()}, nil
	case custodyUnavailable:
		if _, err := os.Lstat(identityPath(path)); os.IsNotExist(err) {
			if _, err := os.Lstat(path); err == nil {
				return CustodyReport{Mode: CustodyUnavailable}, nil
			} else if !os.IsNotExist(err) {
				return CustodyReport{}, err
			}
		} else if err != nil {
			return CustodyReport{}, err
		}
		return CustodyReport{Mode: CustodyAppPassword}, nil
	case custodyPassword:
		return CustodyReport{Mode: CustodyAppPassword}, nil
	case custodyService:
	}
	host := c.serviceHost(path)
	active, err := host.resolve()
	if err != nil {
		return CustodyReport{}, err
	}
	mode := CustodyIdentityService
	if active == identityProvider(host.wrapped) {
		mode = CustodyAppPassword
	}
	return CustodyReport{Mode: mode, ServiceVerified: true}, nil
}

func (c custody) verifiedHost(path string) (*serviceHostProvider, error) {
	switch c.mode {
	case custodyService:
		return c.serviceHost(path), nil
	case custodyRefused:
		return nil, fmt.Errorf("%w: %w", ErrLauncherRefused, c.refusal)
	default:
		return nil, ErrServiceUnverified
	}
}

// moveToService unwraps the identity with the configured app password,
// proves it opens the vault, stores it with the service, reads it back, and
// only then removes the wrapped copy. An interrupted move leaves both
// copies of one identity, and repeating the move completes it.
func (c custody) moveToService(path string) error {
	host, err := c.verifiedHost(path)
	if err != nil {
		return err
	}
	return sharedVault(path, host).withExclusiveLock(func() error {
		if _, err := os.Lstat(host.wrapped.path); os.IsNotExist(err) {
			if held, holdErr := host.service.holds(); holdErr == nil && held {
				return ErrCustodyUnchanged
			}
		}
		doc, err := host.wrapped.LoadOrCreate(true)
		if err != nil {
			return err
		}
		if err := identityOpensVault(path, doc); err != nil {
			return err
		}
		raw, err := encodeIdentityDocument(doc)
		if err != nil {
			return err
		}
		defer clear(raw)
		if err := host.service.items.create(host.service.account, raw); err != nil && !errors.Is(err, errIdentityExists) {
			return fmt.Errorf("store credential vault identity: %w", err)
		}
		held, err := host.service.LoadOrCreate(true)
		if err != nil {
			return err
		}
		if held != doc {
			return fmt.Errorf("%w: the identity service holds a different identity for this vault", ErrVaultCorrupt)
		}
		return host.wrapped.Purge()
	})
}

// moveToAppPassword wraps the service's identity with the configured app
// password, proves the wrapped copy opens with it, and only then removes the
// identity from the service.
func (c custody) moveToAppPassword(path string) error {
	host, err := c.verifiedHost(path)
	if err != nil {
		return err
	}
	password := configuredUnlockSecret()
	defer clear(password)
	if err := checkNewUnlockSecret(password); err != nil {
		return err
	}
	return sharedVault(path, host).withExclusiveLock(func() error {
		held, err := host.service.holds()
		if err != nil {
			return fmt.Errorf("read credential vault identity: %w", err)
		}
		if !held {
			return ErrCustodyUnchanged
		}
		doc, err := host.service.LoadOrCreate(true)
		if err != nil {
			return err
		}
		if err := identityOpensVault(path, doc); err != nil {
			return err
		}
		if err := host.wrapped.commit(doc, password); err != nil {
			return err
		}
		wrapped, err := host.wrapped.LoadOrCreate(true)
		if err != nil {
			return err
		}
		if wrapped != doc {
			return fmt.Errorf("%w: the wrapped identity differs from the service's", ErrVaultCorrupt)
		}
		return host.service.Purge()
	})
}

// fixedIdentityProvider supplies one identity document already in hand.
type fixedIdentityProvider struct{ doc identityDocument }

func (p fixedIdentityProvider) LoadOrCreate(bool) (identityDocument, error) { return p.doc, nil }
func (fixedIdentityProvider) Purge() error                                  { return nil }
func (fixedIdentityProvider) Describe() string                              { return "identity being moved" }

// identityOpensVault proves doc decrypts the vault at path; a vault that
// does not exist yet is opened by any identity.
func identityOpensVault(path string, doc identityDocument) error {
	_, err := (&vaultFile{path: path, provider: fixedIdentityProvider{doc: doc}}).read()
	return err
}

// holds reports whether the custodian has an identity for the account.
func (p *itemsIdentityProvider) holds() (bool, error) {
	raw, err := p.items.read(p.account)
	clear(raw)
	if errors.Is(err, errIdentityMissing) {
		return false, nil
	}
	return err == nil, err
}
