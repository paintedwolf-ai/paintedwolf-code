//go:build !darwin && !linux

package credentialstore

import "errors"

// unattendedIdentity is false until this platform has a native custodian.
func unattendedIdentity() bool { return false }

func verifiedLauncherPID() int { return 0 }

func selectIdentityProvider(path string, isolated, release bool) identityProvider {
	if isolated || !release {
		return newDevelopmentIdentityProvider(path)
	}
	return unavailableIdentityProvider{refusal: errors.Join(ErrCustodianUnavailable, ErrCustodianUnsupported)}
}

func describeCustody(string) (CustodyReport, error) {
	return CustodyReport{Mode: CustodyUnavailable}, nil
}
