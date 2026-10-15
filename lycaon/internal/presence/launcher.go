package presence

import "errors"

// ErrLauncherUnverified: the process that started this engine is not proven
// to be the signed desktop app, so a key it supplied proves nothing.
var ErrLauncherUnverified = errors.New("the engine's launcher is unverified")

// LauncherEvidence is what the engine can show about the process that
// started it. Each platform verifies with the part it can trust.
type LauncherEvidence struct {
	// BundleID names the desktop app whose code signature macOS checks on
	// the parent process.
	BundleID string
	// CustodianVerifiedParentPID is the parent an identity custodian outside
	// this process verified as the installed desktop app while releasing
	// the vault identity; 0 when no custodian verified the launch.
	CustodianVerifiedParentPID int
}

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
