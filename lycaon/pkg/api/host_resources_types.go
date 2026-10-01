package api

// HostResourceStatus is the live discovery verdict for one host resource.
type HostResourceStatus string

const (
	HostResourceStatusAvailable   HostResourceStatus = "available"
	HostResourceStatusUnavailable HostResourceStatus = "unavailable"
	HostResourceStatusUnknown     HostResourceStatus = "unknown"
)

// HostResourceHostSupport is the current build's ability to safely realize the
// selected platform implementation.
type HostResourceHostSupport string

const (
	HostResourceHostSupported     HostResourceHostSupport = "supported"
	HostResourceHostUnsupported   HostResourceHostSupport = "unsupported"
	HostResourceHostNotApplicable HostResourceHostSupport = "not_applicable"
)

// HostResourceExecutionSurface is a portable agent-tool requirement.
type HostResourceExecutionSurface string

const HostResourceSurfaceProcessExec HostResourceExecutionSurface = "process_exec"

// HostResourceAccess is the additive organizational policy for a host resource.
type HostResourceAccess string

const (
	HostResourceAccessAllow HostResourceAccess = "allow"
	HostResourceAccessAsk   HostResourceAccess = "ask"
	HostResourceAccessDeny  HostResourceAccess = "deny"
)

// HostResourceAccessSetting is the editable exact-rule posture. Inherit removes
// the rule from this overlay without weakening any broader effective policy.
type HostResourceAccessSetting string

const (
	HostResourceAccessSettingInherit HostResourceAccessSetting = "inherit"
	HostResourceAccessSettingAsk     HostResourceAccessSetting = "ask"
	HostResourceAccessSettingDeny    HostResourceAccessSetting = "deny"
)

// HostResourcePromptMode is ambient prompt guidance. Write agents also receive
// available, host-supported resources regardless of omit.
type HostResourcePromptMode string

const (
	HostResourcePromptOmit      HostResourcePromptMode = "omit"
	HostResourcePromptAdvertise HostResourcePromptMode = "advertise"
	HostResourcePromptAvoid     HostResourcePromptMode = "avoid"
)

// HostResourceConnectionMode is the connection route associated with a host resource.
type HostResourceConnectionMode string

const (
	HostResourceConnectionNone             HostResourceConnectionMode = "none"
	HostResourceConnectionProxy            HostResourceConnectionMode = "proxy"
	HostResourceConnectionSOCKS            HostResourceConnectionMode = "socks"
	HostResourceConnectionBaselineLoopback HostResourceConnectionMode = "baseline_loopback"
	HostResourceConnectionLocalService     HostResourceConnectionMode = "local_service"
	HostResourceConnectionDirectIP         HostResourceConnectionMode = "direct_ip"
	HostResourceConnectionDynamic          HostResourceConnectionMode = "dynamic"
)

// HostResourceLocalServiceTransport is a platform local-service substrate.
type HostResourceLocalServiceTransport string

const (
	HostResourceLocalServiceUnixSocket HostResourceLocalServiceTransport = "unix_socket"
	HostResourceLocalServiceNamedPipe  HostResourceLocalServiceTransport = "named_pipe"
)
