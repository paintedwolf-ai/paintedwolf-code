//go:build !darwin

package gitcredential

// newBackend serves the vault on platforms without a native keychain bridge.
func newBackend() backend { return VaultBackend() }
