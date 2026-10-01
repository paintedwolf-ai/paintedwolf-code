package api

// ExternalAccessVisibility is a primitive per-effect visibility value.
type ExternalAccessVisibility string

const (
	ExternalAccessVisibilityObserved   ExternalAccessVisibility = "observed"
	ExternalAccessVisibilityDeclared   ExternalAccessVisibility = "declared"
	ExternalAccessVisibilityUnobserved ExternalAccessVisibility = "unobserved"
	ExternalAccessVisibilityNone       ExternalAccessVisibility = "none"
)

// ExternalAccessVisibilitySummary is a derived action-level visibility summary.
type ExternalAccessVisibilitySummary string

const (
	ExternalAccessVisibilitySummaryObserved   ExternalAccessVisibilitySummary = "observed"
	ExternalAccessVisibilitySummaryDeclared   ExternalAccessVisibilitySummary = "declared"
	ExternalAccessVisibilitySummaryUnobserved ExternalAccessVisibilitySummary = "unobserved"
	ExternalAccessVisibilitySummaryMixed      ExternalAccessVisibilitySummary = "mixed"
	ExternalAccessVisibilitySummaryNone       ExternalAccessVisibilitySummary = "none"
	ExternalAccessVisibilitySummaryUnknown    ExternalAccessVisibilitySummary = "unknown"
)

// ExternalAccessMode names an exceptional external-access path on an action.
type ExternalAccessMode string

const (
	ExternalAccessModeMediatedHTTP  ExternalAccessMode = "mediated_http"
	ExternalAccessModeMediatedSocks ExternalAccessMode = "mediated_socks"
	ExternalAccessModeLocalSocket   ExternalAccessMode = "local_socket"
	ExternalAccessModeDirectIP      ExternalAccessMode = "direct_ip"
	ExternalAccessModeFullBypass    ExternalAccessMode = "full_bypass"
)

// ExternalAccessTransport is a mediated transport that observed an endpoint.
type ExternalAccessTransport string

const (
	ExternalAccessTransportHTTPConnect ExternalAccessTransport = "http_connect"
	ExternalAccessTransportHTTPRequest ExternalAccessTransport = "http_request"
	ExternalAccessTransportSocksTCP    ExternalAccessTransport = "socks_tcp"
)

// ExternalAccessDecision is the host decision for an observed mediated endpoint.
type ExternalAccessDecision string

const (
	ExternalAccessDecisionAllow ExternalAccessDecision = "allow"
	ExternalAccessDecisionDeny  ExternalAccessDecision = "deny"
)

// ExternalAccessSocketAuthority is the fixed authority class for applied local sockets.
type ExternalAccessSocketAuthority string

const (
	ExternalAccessSocketAuthorityOutsideSandboxDaemon ExternalAccessSocketAuthority = "outside_sandbox_daemon"
)

// ExternalAccessDirectScope is the fixed scope for one-action direct IP.
type ExternalAccessDirectScope string

const (
	ExternalAccessDirectScopeCurrentAction ExternalAccessDirectScope = "current_action"
)
