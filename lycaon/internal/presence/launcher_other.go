//go:build !darwin

package presence

import "fmt"

// VerifyLauncher has no signed-launcher check on this platform. The vault
// there is wrapped by the app password, so no caller of this check exists
// outside macOS.
func VerifyLauncher(string) error {
	return fmt.Errorf("%w: this platform has no signed-launcher check", ErrLauncherUnverified)
}
