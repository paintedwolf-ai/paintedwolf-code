//go:build !darwin && !linux

package presence

import "fmt"

// VerifyLauncher has no signed-launcher check on this platform. The vault
// there is wrapped by the app password, so no caller of this check exists
// outside macOS and Linux.
func VerifyLauncher(LauncherEvidence) error {
	return fmt.Errorf("%w: this platform has no signed-launcher check", ErrLauncherUnverified)
}
