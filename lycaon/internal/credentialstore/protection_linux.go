package credentialstore

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// VerifyIdentityProtection checks the identity service with disposable identities.
func VerifyIdentityProtection() error {
	if isolatedProcess() || !releaseBuild() {
		return fmt.Errorf("credential protection verification requires the release vault identity service provider")
	}
	return linuxCustody().verifyIdentityProtection()
}

func (c custody) verifyIdentityProtection() (err error) {
	switch c.mode {
	case custodyRefused:
		return errors.Join(ErrLauncherRefused, c.refusal)
	case custodyUnavailable:
		return errors.Join(ErrCustodianUnavailable, c.refusal)
	case custodyService:
	}
	a := c.provider("protection-probe/" + uuid.NewString()).(*itemsIdentityProvider)
	b := c.provider("protection-probe/" + uuid.NewString()).(*itemsIdentityProvider)
	defer func() { err = errors.Join(err, a.Purge(), b.Purge()) }()
	return verifyDisposableIdentities(a, b)
}
