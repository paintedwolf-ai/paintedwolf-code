package credentialstore

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// VerifyIdentityProtection checks native Keychain authority with disposable identities.
func VerifyIdentityProtection() (err error) {
	a, ok := selectIdentityProvider("protection-probe/"+uuid.NewString(), isolatedProcess(), releaseBuild()).(*itemsIdentityProvider)
	if !ok {
		return fmt.Errorf("credential protection verification requires the release Keychain provider")
	}
	b := newKeychainIdentityProvider("protection-probe/" + uuid.NewString()).(*itemsIdentityProvider)
	defer func() { err = errors.Join(err, a.Purge(), b.Purge()) }()
	return verifyDisposableIdentities(a, b)
}
