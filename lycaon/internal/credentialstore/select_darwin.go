package credentialstore

// unattendedIdentityProvider: the release Keychain identity needs no password.
const unattendedIdentityProvider = true

func selectIdentityProvider(path string, isolated, release bool) identityProvider {
	if isolated || !release {
		return newDevelopmentIdentityProvider(path)
	}
	return newKeychainIdentityProvider(path)
}
