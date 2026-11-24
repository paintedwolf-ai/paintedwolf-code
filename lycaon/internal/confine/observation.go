package confine

import (
	"slices"
	"strings"
)

const (
	SignalSocksProxy                     = "socks_proxy"
	SignalBoundaryRefused                = "boundary_refused"
	SignalRemotePackageDestinationDenied = "remote_package_destination_denied"
	DenialSubjectPackageHost             = "remote_package_destination"
)

// Observation holds boundary facts from one invocation.
type Observation struct {
	Applied       bool
	Network       string
	DenialSubject string
	Destination   string
	// Port is the broker-observed destination port.
	Port    int
	Signals []string
	// FailedStages are the command lines of the finished invocation's failed
	// stages; empty while it runs.
	FailedStages []string
	// SucceededStages are the command lines of stages that completed with exit 0.
	SucceededStages []string
	// Running reports an invocation observed before it ended.
	Running bool
	// TerminationReason is the executor-observed terminal state, if known.
	TerminationReason string
	// Refusals is what the kernel reported refusing the invocation.
	Refusals SandboxRefusals
	// GitTransports records ordered host-evidenced transport outcomes per network Git execution.
	GitTransports []GitTransportObservation
	// SSHRecoveries records evidenced SSH trust and signing observations per SSH stage.
	SSHRecoveries []SSHRecoveryObservation
}

// Merge combines boundary observations across stages of one invocation.
func (o Observation) Merge(other Observation) Observation {
	if !other.Applied {
		return o
	}
	if !o.Applied {
		return other
	}
	merged := o
	merged.Signals = append([]string(nil), o.Signals...)
	merged.FailedStages = append([]string(nil), o.FailedStages...)
	merged.SucceededStages = append([]string(nil), o.SucceededStages...)
	if merged.Network == "" {
		merged.Network = other.Network
	}
	if merged.DenialSubject == "" {
		merged.DenialSubject = other.DenialSubject
	}
	if merged.Destination == "" && other.Destination != "" {
		merged.Destination = other.Destination
		merged.Port = other.Port
	}
	for _, signal := range other.Signals {
		merged.add(signal)
	}
	for _, stage := range other.FailedStages {
		if stage != "" && !slices.Contains(merged.FailedStages, stage) {
			merged.FailedStages = append(merged.FailedStages, stage)
		}
	}
	for _, stage := range other.SucceededStages {
		if stage != "" && !slices.Contains(merged.SucceededStages, stage) {
			merged.SucceededStages = append(merged.SucceededStages, stage)
		}
	}
	merged.Running = o.Running || other.Running
	if merged.TerminationReason == "" || (other.TerminationReason != "" && other.TerminationReason != "exited") {
		merged.TerminationReason = other.TerminationReason
	}
	oSucceededOnly := len(o.SucceededStages) > 0 && len(o.FailedStages) == 0 && !o.Running
	otherSucceededOnly := len(other.SucceededStages) > 0 && len(other.FailedStages) == 0 && !other.Running
	switch {
	case oSucceededOnly && (len(other.FailedStages) > 0 || other.Running):
		merged.Refusals = other.Refusals
	case otherSucceededOnly && (len(o.FailedStages) > 0 || o.Running):
		merged.Refusals = o.Refusals
	default:
		merged.Refusals = o.Refusals.Merge(other.Refusals)
	}
	if len(o.GitTransports) > 0 || len(other.GitTransports) > 0 {
		merged.GitTransports = make([]GitTransportObservation, 0, len(o.GitTransports)+len(other.GitTransports))
		for _, tr := range o.GitTransports {
			merged.GitTransports = append(merged.GitTransports, tr.Clone())
		}
		for _, tr := range other.GitTransports {
			merged.GitTransports = append(merged.GitTransports, tr.Clone())
		}
	}
	if len(o.SSHRecoveries) > 0 || len(other.SSHRecoveries) > 0 {
		merged.SSHRecoveries = make([]SSHRecoveryObservation, 0, len(o.SSHRecoveries)+len(other.SSHRecoveries))
		for _, sr := range o.SSHRecoveries {
			merged.SSHRecoveries = append(merged.SSHRecoveries, sr.Clone())
		}
		for _, sr := range other.SSHRecoveries {
			merged.SSHRecoveries = append(merged.SSHRecoveries, sr.Clone())
		}
	}
	return merged
}

// HasSignal reports whether signal was observed on this invocation.
func (o Observation) HasSignal(signal string) bool {
	signal = strings.TrimSpace(signal)
	if signal == "" {
		return false
	}
	for _, existing := range o.Signals {
		if existing == signal {
			return true
		}
	}
	return false
}

func (o *Observation) add(signal string) {
	signal = strings.TrimSpace(signal)
	if signal == "" || o.HasSignal(signal) {
		return
	}
	o.Signals = append(o.Signals, signal)
}

// GitTransportCause classifies the host-evidenced transport outcome of a network Git execution.
type GitTransportCause string

const (
	GitTransportCauseNone           GitTransportCause = ""
	GitTransportCauseEstablished    GitTransportCause = "established"
	GitTransportCauseSetupFailed    GitTransportCause = "setup_failed"
	GitTransportCauseLeaseEnded     GitTransportCause = "lease_ended"
	GitTransportCauseEndpointDenied GitTransportCause = "endpoint_denied"
	GitTransportCauseDialFailed     GitTransportCause = "dial_failed"
	GitTransportCauseInvalidRequest GitTransportCause = "invalid_request"
	GitTransportCauseUnknown        GitTransportCause = "unknown"
)

// GitTransportAttempt records one verified SSH connect attempt mediated by the host.
type GitTransportAttempt struct {
	Ordinal         int    `json:"ordinal"`
	Destination     string `json:"destination,omitempty"`
	Host            string `json:"host,omitempty"`
	Port            uint16 `json:"port,omitempty"`
	Stage           string `json:"stage"`
	Outcome         string `json:"outcome"`
	ReplyCode       uint8  `json:"reply_code"`
	PolicyReason    string `json:"policy_reason,omitempty"`
	ErrorClass      string `json:"error_class,omitempty"`
	SawServerBanner bool   `json:"saw_server_banner,omitempty"`
	ReachedUserAuth bool   `json:"reached_user_auth,omitempty"`
}

// GitTransportObservation records the ordered transport attempts and summary cause for one Git child execution.
type GitTransportObservation struct {
	ExecutionID  string                `json:"execution_id,omitempty"`
	Command      string                `json:"command,omitempty"`
	ExitCode     int                   `json:"exit_code"`
	Cause        GitTransportCause     `json:"cause"`
	Stage        string                `json:"stage,omitempty"`
	Destination  string                `json:"destination,omitempty"`
	Host         string                `json:"host,omitempty"`
	Port         uint16                `json:"port,omitempty"`
	PolicyReason string                `json:"policy_reason,omitempty"`
	ErrorClass   string                `json:"error_class,omitempty"`
	Attempts     []GitTransportAttempt `json:"attempts,omitempty"`
}

// Clone returns a deep copy of the transport observation.
func (o GitTransportObservation) Clone() GitTransportObservation {
	out := o
	out.Attempts = slices.Clone(o.Attempts)
	return out
}

// PreAuthFailure reports whether host mediation failed before an upstream SSH stream opened.
func (c GitTransportCause) PreAuthFailure() bool {
	switch c {
	case GitTransportCauseSetupFailed, GitTransportCauseLeaseEnded, GitTransportCauseEndpointDenied, GitTransportCauseDialFailed, GitTransportCauseInvalidRequest:
		return true
	default:
		return false
	}
}

// SummarizeGitTransport derives the execution-level transport summary while retaining every verified attempt in order.
func SummarizeGitTransport(executionID, command string, exitCode int, failed bool, setupStage, setupReason, setupDestination, setupHost string, setupPort uint16, attempts []GitTransportAttempt) GitTransportObservation {
	obs := GitTransportObservation{
		ExecutionID: executionID,
		Command:     command,
		ExitCode:    exitCode,
		Attempts:    slices.Clone(attempts),
	}
	if setupReason != "" {
		obs.Cause = GitTransportCauseSetupFailed
		obs.Stage = setupStage
		if obs.Stage == "" {
			obs.Stage = "host_setup"
		}
		obs.Destination = setupDestination
		obs.Host = setupHost
		obs.Port = setupPort
		obs.PolicyReason = setupReason
		return obs
	}
	for _, attempt := range attempts {
		if attempt.Outcome != string(GitTransportCauseEstablished) {
			obs.Cause = GitTransportCause(attempt.Outcome)
			obs.Stage = attempt.Stage
			obs.Destination = attempt.Destination
			obs.Host = attempt.Host
			obs.Port = attempt.Port
			obs.PolicyReason = attempt.PolicyReason
			obs.ErrorClass = attempt.ErrorClass
			return obs
		}
	}
	if len(attempts) > 0 {
		last := attempts[len(attempts)-1]
		obs.Cause = GitTransportCauseEstablished
		obs.Stage = last.Stage
		obs.Destination = last.Destination
		obs.Host = last.Host
		obs.Port = last.Port
		return obs
	}
	if failed {
		obs.Cause = GitTransportCauseUnknown
	}
	return obs
}

// SSHFailureCause names a host-evidenced SSH trust or signing failure on one stage.
type SSHFailureCause string

const (
	SSHFailureNone              SSHFailureCause = ""
	SSHFailureHostKeyUnreadable SSHFailureCause = "host_key_unreadable"
	SSHFailureHostKeyChanged    SSHFailureCause = "host_key_changed"
	SSHFailureHostKeyUnverified SSHFailureCause = "host_key_unverified"
	SSHFailureAgentRequired     SSHFailureCause = "agent_required"
	SSHFailureAgentUnavailable  SSHFailureCause = "agent_unavailable"
	SSHFailurePublicKeyRejected SSHFailureCause = "publickey_rejected"
)

// SSHRecoveryObservation records evidenced SSH trust and signing facts for one stage.
type SSHRecoveryObservation struct {
	Stage          string          `json:"stage,omitempty"`
	Client         string          `json:"client,omitempty"`
	Destination    string          `json:"destination,omitempty"`
	Cause          SSHFailureCause `json:"cause,omitempty"`
	TrustPath      string          `json:"trust_path,omitempty"`
	AgentSocket    string          `json:"agent_socket,omitempty"`
	AgentSocketRef string          `json:"agent_socket_ref,omitempty"`
	ProbedPaths    []string        `json:"probed_paths,omitempty"`
}

// Clone returns a deep copy of the SSH recovery observation.
func (o SSHRecoveryObservation) Clone() SSHRecoveryObservation {
	out := o
	out.ProbedPaths = slices.Clone(o.ProbedPaths)
	return out
}
