package confine

import "context"

const (
	RemotePackageEnvironmentReduced       = "reduced"
	RemotePackageNetworkScopeRegistryOnly = "registry_only"
)

// Report describes the applied process boundary and refusal attribution.
type Report struct {
	// Confined reports whether the OS sandbox was applied.
	Confined       bool `json:"confined"`
	ProcessControl bool `json:"process_control,omitempty"`
	HostExecution  bool `json:"host_execution,omitempty"`
	// NetworkMode is the applied egress boundary.
	NetworkMode string `json:"network_mode,omitempty"`
	// SocksProxy reports whether the process received ALL_PROXY.
	SocksProxy     bool   `json:"socks_proxy,omitempty"`
	NetworkPosture string `json:"network_posture,omitempty"`
	// BoundaryRefusal states confinement attribution for a failure.
	BoundaryRefusal string `json:"boundary_refusal,omitempty"`
	// ListenGranted and ListenPorts describe local listener authority.
	ListenGranted bool     `json:"local_listen,omitempty"`
	ListenPorts   []uint16 `json:"local_listen_ports,omitempty"`
	// LoopbackGranted and LoopbackPorts are the same for services on this machine.
	LoopbackGranted bool     `json:"loopback_connect,omitempty"`
	LoopbackPorts   []uint16 `json:"loopback_connect_ports,omitempty"`
	// RemotePackageExecution describes the downloaded-code boundary.
	RemotePackageExecution *RemotePackageExecutionBoundary `json:"remote_package_execution,omitempty"`
	// SandboxRefusals is what the kernel reported refusing the invocation.
	SandboxRefusals []SandboxRefusal `json:"sandbox_refusals,omitempty"`
	// SandboxRefusalsOmitted counts distinct refusals past the retained bound.
	SandboxRefusalsOmitted int `json:"sandbox_refusals_omitted,omitempty"`
	// SandboxRefusalWitness omits WitnessKernel; silent report loss remains possible.
	SandboxRefusalWitness RefusalWitness `json:"sandbox_refusal_witness,omitempty"`
}

// RemotePackageExecutionBoundary states the downloaded-code restrictions.
type RemotePackageExecutionBoundary struct {
	Environment               string   `json:"environment"`
	AmbientCredentialsRemoved bool     `json:"ambient_credentials_removed"`
	ProtectedReadsDenied      bool     `json:"protected_reads_denied"`
	ApprovedReadPaths         []string `json:"approved_read_paths,omitempty"`
	NetworkScope              string   `json:"network_scope"`
	AllowedHosts              []string `json:"allowed_hosts"`
}

// NewRemotePackageExecutionBoundary builds the fixed downloaded-code boundary.
func NewRemotePackageExecutionBoundary(allowedHosts, approvedReadPaths []string) *RemotePackageExecutionBoundary {
	hosts := NormalizeDeclaredHosts(allowedHosts)
	if hosts == nil {
		hosts = []string{}
	}
	return &RemotePackageExecutionBoundary{
		Environment:               RemotePackageEnvironmentReduced,
		AmbientCredentialsRemoved: true,
		ProtectedReadsDenied:      true,
		ApprovedReadPaths:         append([]string(nil), approvedReadPaths...),
		NetworkScope:              RemotePackageNetworkScopeRegistryOnly,
		AllowedHosts:              hosts,
	}
}

// ReportOf builds an applied boundary report.
func ReportOf(b Boundary) Report {
	if b.HostExecution {
		return Report{HostExecution: true, NetworkMode: "direct_ip"}
	}
	if !b.Applied {
		return Report{}
	}
	return Report{
		ProcessControl: b.ProcessControl,
		Confined:       true,
		NetworkMode:    NetworkLabel(b.Network),
		SocksProxy:     b.SocksProxyEnv,
	}
}

// SpawnFacts holds a spawn report and the action's live observations.
type SpawnFacts struct {
	Report Report
	// Network includes allowed and denied broker observations.
	Network func() []EgressHost
	// Action is the spawned action's lease; nil for an unbound spawn.
	Action *ActionLease
}

// MediatedNetwork returns an isolated snapshot of attributed destinations.
func (s SpawnFacts) MediatedNetwork() []EgressHost {
	if s.Network == nil {
		return nil
	}
	return append([]EgressHost(nil), s.Network()...)
}

// Refusals is what the kernel has reported refusing the running action.
func (s SpawnFacts) Refusals() SandboxRefusals { return s.Action.Refusals() }

// SettledRefusals returns collected reports after the settle probe.
func (s SpawnFacts) SettledRefusals(ctx context.Context) SandboxRefusals {
	return s.Action.SettledRefusals(ctx)
}

// WithSandboxRefusals states what the kernel refused a confined invocation.
func (r Report) WithSandboxRefusals(refusals SandboxRefusals) Report {
	if !r.Confined {
		return r
	}
	r.SandboxRefusals = append([]SandboxRefusal(nil), refusals.Refusals...)
	r.SandboxRefusalsOmitted = refusals.Omitted
	r.SandboxRefusalWitness = ""
	if refusals.Witness != WitnessKernel {
		r.SandboxRefusalWitness = refusals.Witness
	}
	return r
}

// LocalNetworkGrant describes listener and loopback authority.
type LocalNetworkGrant struct {
	Listen        bool
	ListenPorts   []uint16
	Loopback      bool
	LoopbackPorts []uint16
}

// WithLocalNetwork states the local-network grants applied to this spawn.
func (r Report) WithLocalNetwork(g LocalNetworkGrant) Report {
	if r.HostExecution {
		return r
	}
	r.ListenGranted = g.Listen
	r.ListenPorts = append([]uint16(nil), g.ListenPorts...)
	r.LoopbackGranted = g.Loopback
	r.LoopbackPorts = append([]uint16(nil), g.LoopbackPorts...)
	return r
}

// WithRemotePackageExecution adds the downloaded-code boundary.
func (r Report) WithRemotePackageExecution(allowedHosts, approvedReadPaths []string) Report {
	if r.HostExecution {
		return r
	}
	r.RemotePackageExecution = NewRemotePackageExecutionBoundary(allowedHosts, approvedReadPaths)
	return r
}
