//go:build !darwin

package credentialstore

func selectIdentityProvider(path string, isolated, release bool) identityProvider {
	if isolated || !release {
		return newDevelopmentIdentityProvider(path)
	}
	return newPassphraseIdentityProvider(path)
}
