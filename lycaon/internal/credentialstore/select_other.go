//go:build !darwin

package credentialstore

// unattendedIdentityProvider: the release identity is wrapped by the app password.
const unattendedIdentityProvider = false

func selectIdentityProvider(path string, isolated, release bool) identityProvider {
	if isolated || !release {
		return newDevelopmentIdentityProvider(path)
	}
	return newPassphraseIdentityProvider(path)
}
