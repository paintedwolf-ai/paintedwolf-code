package credentialstore

import "github.com/lycaon/lycaon/internal/version"

const keychainService = version.BundleID + ".credential-vault"

func newKeychainIdentityProvider(path string) identityProvider {
	return &itemsIdentityProvider{
		account: identityAccount(path), description: "device-only identity in macOS data protection Keychain",
		items: dataProtectionItems{},
	}
}
