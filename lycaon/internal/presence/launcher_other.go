//go:build !darwin && !linux

package presence

import "fmt"

// VerifyLauncher refuses until this platform has a native launcher verifier.
// Release credential custody is also unavailable on this platform.
func VerifyLauncher(LauncherEvidence) error {
	return fmt.Errorf("%w: this platform has no signed-launcher check", ErrLauncherUnverified)
}
