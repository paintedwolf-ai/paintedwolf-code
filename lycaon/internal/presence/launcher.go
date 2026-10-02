package presence

import "errors"

// ErrLauncherUnverified: the process that started this engine is not proven
// to be the signed desktop app, so a key it supplied proves nothing.
var ErrLauncherUnverified = errors.New("the engine's launcher is unverified")

// TrustedKey returns the verification key the engine may install. When the
// vault opens without a human secret, any process able to start the signed
// engine could supply its own key, so the key is accepted only from a
// verified launcher; otherwise launching the engine gains nothing, because
// the vault stays locked without the app password.
func TrustedKey(key string, unattendedVault bool, verifyLauncher func() error) (string, error) {
	if key == "" || !unattendedVault {
		return key, nil
	}
	if err := verifyLauncher(); err != nil {
		return "", err
	}
	return key, nil
}
