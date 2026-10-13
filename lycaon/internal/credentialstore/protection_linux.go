package credentialstore

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// VerifyIdentityProtection checks the identity service with disposable identities.
func VerifyIdentityProtection() (err error) {
	a, ok := selectIdentityProvider("protection-probe/"+uuid.NewString(), isolatedProcess(), releaseBuild()).(*itemsIdentityProvider)
	if !ok {
		return fmt.Errorf("credential protection verification requires the vault identity service")
	}
	b := linuxCustody().provider("protection-probe/" + uuid.NewString()).(*itemsIdentityProvider)
	defer func() { err = errors.Join(err, a.Purge(), b.Purge()) }()
	return verifyDisposableIdentities(a, b)
}
