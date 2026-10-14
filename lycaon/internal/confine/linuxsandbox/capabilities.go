package linuxsandbox

import (
	"encoding/json"
	"errors"
	"strings"
)

// LandlockABIFloor is the oldest Landlock ABI the helper applies; it brings
// file truncation under the ruleset.
const LandlockABIFloor = 3

// AppArmorProfile is the profile the Linux package installs so the engine may
// create user namespaces on hosts that restrict them.
const AppArmorProfile = "painted-wolf-code"

// Capabilities are the kernel and policy facts Linux confinement depends on,
// so the host can say why confinement is unavailable.
type Capabilities struct {
	// UserNamespaces reports that the helper created its namespaces.
	UserNamespaces bool `json:"user_namespaces"`
	LandlockABI    int  `json:"landlock_abi"`
	Seccomp        bool `json:"seccomp"`
	Pidfd          bool `json:"pidfd"`
	// AppArmorRestrictsUserns is kernel.apparmor_restrict_unprivileged_userns.
	AppArmorRestrictsUserns bool `json:"apparmor_restricts_userns"`
	// AppArmorLabel is the engine's own confinement label, when AppArmor runs.
	AppArmorLabel    string `json:"apparmor_label,omitempty"`
	SELinuxEnforcing bool   `json:"selinux_enforcing"`
	// Applied reports that a helper run established every boundary and attested it.
	Applied bool `json:"applied"`
	// Unavailable lists why confinement is not available; empty when it is.
	Unavailable []Unavailability `json:"unavailable,omitempty"`
}

// Available reports whether confined commands can run on this host.
func (c Capabilities) Available() bool {
	return c.Applied && len(c.Unavailable) == 0
}

// Remedy names what makes confinement available.
type Remedy string

const (
	// RemedyInstallPackage installs the Linux package, whose AppArmor profile
	// permits the engine to create user namespaces.
	RemedyInstallPackage Remedy = "install_package"
	// RemedyEnableUserNamespaces asks the administrator to allow unprivileged
	// user namespaces.
	RemedyEnableUserNamespaces Remedy = "enable_user_namespaces"
	// RemedyUpgradeKernel asks for a kernel at the platform floor.
	RemedyUpgradeKernel Remedy = "upgrade_kernel"
)

// Unavailability is one typed reason confinement cannot be applied.
type Unavailability struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
	Remedy Remedy `json:"remedy,omitempty"`
	// Profile names the AppArmor profile that would lift the restriction.
	Profile string `json:"profile,omitempty"`
}

// Codes an Unavailability carries.
const (
	UnavailableUserNamespacesAppArmor    = "user_namespaces_denied_apparmor"
	UnavailableUserNamespacesDenied      = "user_namespaces_denied"
	UnavailableUserNamespacesUnsupported = "user_namespaces_unsupported"
	UnavailableLandlock                  = "landlock_unavailable"
	UnavailableLandlockABI               = "landlock_abi_below_floor"
	UnavailableSeccomp                   = "seccomp_unavailable"
	UnavailablePidfd                     = "pidfd_unavailable"
	UnavailableNamespaces                = "namespaces_unchanged"
	UnavailableMount                     = "mount_failed"
	UnavailableHelper                    = "helper_failed"
)

func (u *Unavailability) Error() string {
	text := "linux sandbox unavailable: " + u.Code
	if u.Detail != "" {
		text += ": " + u.Detail
	}
	return text
}

// UnavailableJSON renders the reason for the helper's stderr marker line.
func (u *Unavailability) UnavailableJSON() string {
	raw, err := json.Marshal(u)
	if err != nil {
		return `{"code":"` + u.Code + `"}`
	}
	return string(raw)
}

// UnavailableMarker heads the helper's stderr line that carries a typed
// unavailability; the JSON reason follows it.
const UnavailableMarker = "unavailable "

// ParseUnavailable reads the typed reason from helper stderr; ok is false when
// no marker line is present.
func ParseUnavailable(stderr string) (Unavailability, bool) {
	for _, line := range strings.Split(stderr, "\n") {
		i := strings.Index(line, UnavailableMarker)
		if i < 0 {
			continue
		}
		var u Unavailability
		if json.Unmarshal([]byte(line[i+len(UnavailableMarker):]), &u) == nil && u.Code != "" {
			return u, true
		}
	}
	return Unavailability{}, false
}

// AsUnavailable extracts a typed unavailability from err.
func AsUnavailable(err error) (*Unavailability, bool) {
	var u *Unavailability
	if errors.As(err, &u) {
		return u, true
	}
	return nil, false
}

// RecordRun folds the outcome of a probe run of the helper into the facts:
// a clean exit proves application; a typed refusal names the gap.
func (c *Capabilities) RecordRun(runErr error, stderr string) {
	if runErr == nil {
		c.Applied = true
		c.UserNamespaces = true
		return
	}
	if u, ok := ParseUnavailable(stderr); ok {
		c.Unavailable = append(c.Unavailable, u)
		return
	}
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = runErr.Error()
	}
	c.Unavailable = append(c.Unavailable, Unavailability{Code: UnavailableHelper, Detail: detail})
}
