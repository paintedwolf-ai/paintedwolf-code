package credentialstore

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// VerifyIdentityProtection checks the identity service with disposable identities.
func VerifyIdentityProtection() (err error) {
	if isolatedProcess() || !releaseBuild() {
		return fmt.Errorf("credential protection verification requires the release vault identity service provider")
	}
	custody := linuxCustody()
	switch custody.mode {
	case custodyRefused:
		return custody.refusal
	case custodyPassword:
		return fmt.Errorf("credential protection verification requires the vault identity service; this host is on the app-password path")
	}
	a := custody.provider("protection-probe/" + uuid.NewString()).(*itemsIdentityProvider)
	b := custody.provider("protection-probe/" + uuid.NewString()).(*itemsIdentityProvider)
	defer func() { err = errors.Join(err, a.Purge(), b.Purge()) }()
	return verifyDisposableIdentities(a, b)
}
