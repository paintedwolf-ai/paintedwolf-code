// Package isolation defines the closed outcome vocabulary for agent isolation.
package isolation

// Disposition says who or what can move past an isolation stop.
type Disposition string

const (
	// DispositionRetry allows a corrected invocation to reach approval.
	DispositionRetry Disposition = "retry"
	// DispositionHumanDecision means the person declined the exact authority.
	DispositionHumanDecision Disposition = "human_decision"
	// DispositionControlPlane is the sole system-terminal authorization outcome.
	DispositionControlPlane Disposition = "control_plane"
)

// Outcome binds an agent-public code to its authorization disposition.
type Outcome struct {
	Code        string
	Disposition Disposition
}

// Retryable reports whether a corrected invocation may retry.
func (o Outcome) Retryable() bool {
	return o.Disposition == DispositionRetry
}

// Agent isolation outcome codes.
const (
	CodeExecutionBoundaryUnavailable       = "SANDBOX_EXECUTION_BOUNDARY_UNAVAILABLE"
	CodeExecutionCapabilityDenied          = "SANDBOX_EXECUTION_CAPABILITY_DENIED"
	CodeExecutionAuthorizationChanged      = "SANDBOX_EXECUTION_AUTHORIZATION_CHANGED"
	CodeApprovalUnavailable                = "SANDBOX_APPROVAL_UNAVAILABLE"
	CodeBoundaryRefused                    = "SANDBOX_BOUNDARY_REFUSED"
	CodeCapabilityRequestInvalid           = "SANDBOX_CAPABILITY_REQUEST_INVALID"
	CodeControlPlaneDenied                 = "SANDBOX_CONTROL_PLANE_DENIED"
	CodeDirectIPDenied                     = "SANDBOX_DIRECT_IP_DENIED"
	CodeDirectIPAuthorizationChanged       = "SANDBOX_DIRECT_IP_AUTHORIZATION_CHANGED"
	CodeDirectIPRequestInvalid             = "SANDBOX_DIRECT_IP_REQUEST_INVALID"
	CodeLocalListenDenied                  = "SANDBOX_LOCAL_LISTEN_DENIED"
	CodeLocalListenRequestInvalid          = "SANDBOX_LOCAL_LISTEN_REQUEST_INVALID"
	CodeLocalNetworkDenied                 = "SANDBOX_LOCAL_NETWORK_DENIED"
	CodeLoopbackConnectDenied              = "SANDBOX_LOOPBACK_CONNECT_DENIED"
	CodeLoopbackConnectRequestInvalid      = "SANDBOX_LOOPBACK_CONNECT_REQUEST_INVALID"
	CodeReadPathDenied                     = "SANDBOX_READ_PATH_DENIED"
	CodeSocketPathDenied                   = "SANDBOX_SOCKET_PATH_DENIED"
	CodeSocketPathChanged                  = "SANDBOX_SOCKET_PATH_CHANGED"
	CodeSocketPathInvalid                  = "SANDBOX_SOCKET_PATH_INVALID"
	CodeSocketPathLimit                    = "SANDBOX_SOCKET_PATH_LIMIT"
	CodeSocketPathNotFound                 = "SANDBOX_SOCKET_PATH_NOT_FOUND"
	CodeSocketPathNotSocket                = "SANDBOX_SOCKET_PATH_NOT_SOCKET"
	CodeSocketPathRefused                  = "SANDBOX_SOCKET_PATH_REFUSED"
	CodeSocksProxyInvalid                  = "SANDBOX_SOCKS_PROXY_INVALID"
	CodeTryHostExecution                   = "SANDBOX_TRY_HOST_EXECUTION"
	CodeTryLocalNetwork                    = "SANDBOX_TRY_LOCAL_NETWORK"
	CodeTryLoopbackConnect                 = "SANDBOX_TRY_LOOPBACK_CONNECT"
	CodeTryProcessControl                  = "SANDBOX_TRY_PROCESS_CONTROL"
	CodeTryReadPath                        = "SANDBOX_TRY_READ_PATH"
	CodeTrySocketPath                      = "SANDBOX_TRY_SOCKET_PATH"
	CodeTryWriteRoot                       = "SANDBOX_TRY_WRITE_ROOT"
	CodeWriteRootDenied                    = "SANDBOX_WRITE_ROOT_DENIED"
	CodeWorktreeBehindIndex                = "SANDBOX_WORKTREE_BEHIND_INDEX"
	CodeRemotePackageCapabilityDenied      = "REMOTE_PACKAGE_EXECUTION_CAPABILITY_DENIED"
	CodeRemotePackageDestinationDenied     = "REMOTE_PACKAGE_EXECUTION_DESTINATION_DENIED"
	CodeRemotePackageRequiresFreshBoundary = "REMOTE_PACKAGE_EXECUTION_REQUIRES_FRESH_BOUNDARY"
)

var outcomes = [...]Outcome{
	{CodeExecutionBoundaryUnavailable, DispositionRetry},
	{CodeExecutionCapabilityDenied, DispositionHumanDecision},
	{CodeExecutionAuthorizationChanged, DispositionRetry},
	{CodeApprovalUnavailable, DispositionRetry},
	{CodeBoundaryRefused, DispositionRetry},
	{CodeCapabilityRequestInvalid, DispositionRetry},
	{CodeControlPlaneDenied, DispositionControlPlane},
	{CodeDirectIPDenied, DispositionHumanDecision},
	{CodeDirectIPAuthorizationChanged, DispositionRetry},
	{CodeDirectIPRequestInvalid, DispositionRetry},
	{CodeLocalListenDenied, DispositionHumanDecision},
	{CodeLocalListenRequestInvalid, DispositionRetry},
	{CodeLocalNetworkDenied, DispositionHumanDecision},
	{CodeLoopbackConnectDenied, DispositionHumanDecision},
	{CodeLoopbackConnectRequestInvalid, DispositionRetry},
	{CodeReadPathDenied, DispositionHumanDecision},
	{CodeSocketPathDenied, DispositionHumanDecision},
	{CodeSocketPathChanged, DispositionRetry},
	{CodeSocketPathInvalid, DispositionRetry},
	{CodeSocketPathLimit, DispositionRetry},
	{CodeSocketPathNotFound, DispositionRetry},
	{CodeSocketPathNotSocket, DispositionRetry},
	{CodeSocketPathRefused, DispositionRetry},
	{CodeSocksProxyInvalid, DispositionRetry},
	{CodeTryHostExecution, DispositionRetry},
	{CodeTryLocalNetwork, DispositionRetry},
	{CodeTryLoopbackConnect, DispositionRetry},
	{CodeTryProcessControl, DispositionRetry},
	{CodeTryReadPath, DispositionRetry},
	{CodeTrySocketPath, DispositionRetry},
	{CodeTryWriteRoot, DispositionRetry},
	{CodeWriteRootDenied, DispositionHumanDecision},
	{CodeWorktreeBehindIndex, DispositionRetry},
	{CodeRemotePackageCapabilityDenied, DispositionRetry},
	{CodeRemotePackageDestinationDenied, DispositionRetry},
	{CodeRemotePackageRequiresFreshBoundary, DispositionRetry},
}

// Outcomes returns the closed isolation outcome inventory.
func Outcomes() []Outcome {
	return append([]Outcome(nil), outcomes[:]...)
}

// Lookup returns the registered disposition for code.
func Lookup(code string) (Outcome, bool) {
	for _, outcome := range outcomes {
		if outcome.Code == code {
			return outcome, true
		}
	}
	return Outcome{}, false
}
