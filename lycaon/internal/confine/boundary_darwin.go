//go:build darwin

package confine

import (
	"os"
	"syscall"

	"github.com/lycaon/lycaon/internal/sandboxmark"
)

// renderProfile is the Seatbelt profile on this platform, ending with the
// engine's ownership mark.
func renderProfile(c Confinement) (string, error) {
	profile, err := renderSeatbeltProfile(c)
	if err != nil {
		return "", err
	}
	if own := sandboxmark.OwnPath(); own != "" {
		// Last, so no earlier deny overrides it.
		profile += "(allow file-write-data (literal " + sbplString(own) + "))\n"
	}
	return profile, nil
}

// applyAndExec applies the profile to this process, confirms kernel membership,
// and replaces the process with the target.
func applyAndExec(profile []byte, resolved string, target []string) error {
	if err := applySeatbelt(string(profile)); err != nil {
		return err
	}
	if err := attestConfined(); err != nil {
		return err
	}
	if err := applyProcessAttributes(helperProcessAttributes); err != nil {
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
