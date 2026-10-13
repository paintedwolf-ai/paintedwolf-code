package credentialstore

// unattendedIdentity: the release Keychain identity needs no password.
func unattendedIdentity() bool { return true }

// verifiedLauncherPID: macOS verifies the launcher by code signature, not
// through an identity service.
func verifiedLauncherPID() int { return 0 }

func selectIdentityProvider(path string, isolated, release bool) identityProvider {
	if isolated || !release {
		return newDevelopmentIdentityProvider(path)
	}
	return newKeychainIdentityProvider(path)
}
