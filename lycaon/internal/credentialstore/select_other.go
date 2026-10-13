//go:build !darwin && !linux

package credentialstore

// unattendedIdentity: the release identity is wrapped by the app password.
func unattendedIdentity() bool { return false }

func verifiedLauncherPID() int { return 0 }

func selectIdentityProvider(path string, isolated, release bool) identityProvider {
	if isolated || !release {
		return newDevelopmentIdentityProvider(path)
	}
	return newPassphraseIdentityProvider(path)
}
