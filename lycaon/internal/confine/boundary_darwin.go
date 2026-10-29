//go:build darwin

package confine

import (
	"os"
	"syscall"
)

// renderProfile is the Seatbelt profile on this platform.
func renderProfile(c Confinement) (string, error) { return renderSeatbeltProfile(c) }

// applyAndExec applies the profile to this process, confirms kernel membership,
// and replaces the process with the target.
func applyAndExec(profile []byte, resolved string, target []string) error {
	if err := applySeatbelt(string(profile)); err != nil {
		return err
	}
	if err := attestConfined(); err != nil {
		return err
	}
	if helperPinnedReadyFDArg >= 0 {
		return execNativeTarget(resolved, target)
	}
	if err := syscall.Exec(resolved, target, os.Environ()); err != nil {
		return ClassifyExecError(resolved, err)
	}
	return nil
}
