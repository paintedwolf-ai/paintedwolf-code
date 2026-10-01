// Package hostresources discovers and describes host resources available to the agent.
package hostresources

import "time"

const (
	CatalogVersion      = 1
	CatalogMax          = 256
	CatalogBytesMax     = 1 << 20
	ExpressionDepthMax  = 8
	ExpressionNodesMax  = 64
	ProbeValuesMax      = 16
	RequirementsMax     = 16
	IdentifierBytesMax  = 128
	ExecutableBytesMax  = 255
	EnvironmentBytesMax = 255
	URLBytesMax         = 2048
	PathBytesMax        = 4096
)

// Status is the live availability of one host resource.
type Status string

const (
	StatusAvailable   Status = "available"
	StatusUnavailable Status = "unavailable"
	StatusUnknown     Status = "unknown"
)

// HostSupport says whether this build can safely realize the selected
// platform implementation. It is separate from discovery: detecting a local
// service does not imply that the current confinement backend can grant it.
type HostSupport string

const (
	HostSupported     HostSupport = "supported"
	HostUnsupported   HostSupport = "unsupported"
	HostNotApplicable HostSupport = "not_applicable"
)

// ExecutionSurface is a portable class of agent tool needed to use a
// host resource. Concrete tool names remain an agent-profile detail.
type ExecutionSurface string

const SurfaceProcessExec ExecutionSurface = "process_exec"

// Access is the additional organizational policy applied to a host resource.
// Allow means host-resource policy adds no decision; ordinary sandbox and approval
// policy still apply. Ask adds one approval requirement. Deny is absolute.
type Access string

const (
	AccessAllow Access = "allow"
	AccessAsk   Access = "ask"
	AccessDeny  Access = "deny"
)

// AccessSetting is the editable exact-rule posture for one host resource.
// Inherit means the effective access came from a broader rule or the default.
type AccessSetting string

const (
	AccessSettingInherit AccessSetting = "inherit"
	AccessSettingAsk     AccessSetting = "ask"
	AccessSettingDeny    AccessSetting = "deny"
)

// PromptMode is ambient prompt guidance. Write agents also receive available,
// host-supported resources regardless of omit.
type PromptMode string

const (
	PromptOmit      PromptMode = "omit"
	PromptAdvertise PromptMode = "advertise"
	PromptAvoid     PromptMode = "avoid"
)

// ConnectionMode is the confinement-compatible route used by a host resource.
type ConnectionMode string

const (
	ConnectionNone             ConnectionMode = "none"
	ConnectionProxy            ConnectionMode = "proxy"
	ConnectionSOCKS            ConnectionMode = "socks"
	ConnectionBaselineLoopback ConnectionMode = "baseline_loopback"
	ConnectionLocalService     ConnectionMode = "local_service"
	ConnectionDirectIP         ConnectionMode = "direct_ip"
	ConnectionDynamic          ConnectionMode = "dynamic"
)

// LocalServiceTransport is the substrate selected by one platform
// realization. It is never projected into prompts as authority.
type LocalServiceTransport string

const (
	LocalServiceUnixSocket LocalServiceTransport = "unix_socket"
	LocalServiceNamedPipe  LocalServiceTransport = "named_pipe"
)

// Connection is one host-validated route for an available host resource.
type Connection struct {
	Mode      ConnectionMode        `json:"mode"`
	Transport LocalServiceTransport `json:"transport,omitempty"`
	Target    string                `json:"target,omitempty"`
}

// State is the catalog definition and current machine-state verdict for one host resource.
type State struct {
	ID            string             `json:"id"`
	Family        string             `json:"family"`
	Label         string             `json:"label"`
	Category      string             `json:"category"`
	Description   string             `json:"description"`
	DocsURL       string             `json:"docs_url,omitempty"`
	Origin        string             `json:"origin"`
	Status        Status             `json:"status"`
	HostSupport   HostSupport        `json:"host_support"`
	Access        Access             `json:"access"`
	AccessSetting AccessSetting      `json:"access_setting"`
	Prompt        PromptMode         `json:"prompt"`
	Reason        string             `json:"reason,omitempty"`
	Surfaces      []ExecutionSurface `json:"surfaces"`
	Connections   []Connection       `json:"connections"`
	CheckedAt     time.Time          `json:"checked_at"`
}

// Diagnostic reports an ignored user catalog problem without destabilizing startup.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Snapshot is one coherent discovery result.
type Snapshot struct {
	Version         int          `json:"version"`
	Resources       []State      `json:"resources"`
	Diagnostics     []Diagnostic `json:"diagnostics"`
	UserCatalogPath string       `json:"user_catalog_path"`
	CheckedAt       time.Time    `json:"checked_at"`
	Fingerprint     string       `json:"fingerprint"`
}

// ConnectionRequest is the concrete exceptional authority implied by host-resource ids.
// Proxy, SOCKS, loopback, none, and dynamic routes require no widening.
type ConnectionRequest struct {
	LocalServices      []LocalServiceEndpoint
	DirectDestinations []string
}

// LocalServiceEndpoint is typed exceptional authority resolved from trusted
// device configuration for the current platform.
type LocalServiceEndpoint struct {
	Transport LocalServiceTransport
	Target    string
}

// ActionResolution is the one authoritative host-resource projection used for a
// process start. It retains exact ids for policy/OAR while expanding only
// host-validated connection resources.
type ActionResolution struct {
	States      map[string]State
	Families    []string
	Connections ConnectionRequest
	Ask         []string
	Deny        []string
	// PathExtra holds canonical directories to append to the child's PATH so a
	// resource's helper binaries resolve. Machine-local; never projected onto the wire.
	PathExtra []string
	// WriteRoots are expanded daemon state directories from the matching
	// realization. Internal to execution — not projected onto the wire.
	WriteRoots []string
}
