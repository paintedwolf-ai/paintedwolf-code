package credentialstore

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// VerifyIdentityProtection checks native Keychain authority with disposable identities.
func VerifyIdentityProtection() (err error) {
	a, ok := selectIdentityProvider("protection-probe/"+uuid.NewString(), isolatedProcess(), releaseBuild()).(*keychainIdentityProvider)
	if !ok {
		return fmt.Errorf("credential protection verification requires the release Keychain provider")
	}
	b := newKeychainIdentityProvider("protection-probe/" + uuid.NewString()).(*keychainIdentityProvider)
	defer func() { err = errors.Join(err, a.Purge(), b.Purge()) }()
	first, err := a.LoadOrCreate(false)
	if err != nil {
		return err
	}
	second, err := b.LoadOrCreate(false)
	if err != nil {
		return err
	}
	if first == second {
		return fmt.Errorf("isolated Keychain identities are equal")
	}
	if err := a.Purge(); err != nil {
		return err
	}
	if _, err := a.items.read(a.account); err == nil {
		return errors.New("deleted Keychain identity remains accessible")
	} else if !errors.Is(err, keychainNotFound) {
		return fmt.Errorf("verify deleted Keychain identity: %w", err)
	}
	loaded, err := b.LoadOrCreate(true)
	if err != nil {
		return err
	}
	if loaded != second {
		return fmt.Errorf("deleting another identity changed the retained identity")
	}
	return nil
}
