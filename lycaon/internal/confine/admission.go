package confine

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine/internal/admissionstate"
)

// EnableAutoConfine turns confinement on for this process.
func EnableAutoConfine() { admissionstate.ProcessEnabled.Store(true) }

// BypassEnabled reports the explicit host-wide bypass.
func BypassEnabled() bool {
	return configdir.EnvTruthy(os.Getenv("LYCAON_BYPASS_APPROVALS"))
}

// Enforcing reports whether the host applies confinement.
func Enforcing() bool {
	return confinementRequired() && Available()
}

// confinementRequired is the policy requirement, independent of kernel availability.
func confinementRequired() bool {
	return admissionstate.ProcessEnabled.Load() && !BypassEnabled() && !SandboxDisabled()
}

// ErrNotConfined rejects a missing required boundary.
var ErrNotConfined = errors.New("confine: refusing to run unconfined: the required sandbox is unavailable " +
	"or no valid confinement could be built for this action")

// RequireApplied rejects a missing required boundary, including unavailable platforms.
func RequireApplied(applied bool) error {
	if applied || !confinementRequired() {
		return nil
	}
	if caps, linux := LinuxCapabilities(); linux && len(caps.Unavailable) > 0 {
		return fmt.Errorf("%w: %w", ErrNotConfined, &caps.Unavailable[0])
	}
	return ErrNotConfined
}

// SandboxDisabled reports the explicit confinement off-switch.
func SandboxDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LYCAON_SANDBOX"))) {
	case "off", "0", "false", "no", "disable", "disabled":
		return true
	}
	return false
}
