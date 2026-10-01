//go:build !darwin

package gitcredential

import "errors"

type unsupportedBackend struct{}

func newBackend() backend { return unsupportedBackend{} }

func (unsupportedBackend) Get(Credential) (Credential, bool, error) {
	return Credential{}, false, errors.New("macOS Keychain credential helper is unavailable")
}

func (unsupportedBackend) Store(Credential) error {
	return errors.New("macOS Keychain credential helper is unavailable")
}

func (unsupportedBackend) Erase(Credential) error {
	return errors.New("macOS Keychain credential helper is unavailable")
}
