package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// Egress labels on Contained — locked vocabulary.
const (
	ContainedEgressDeny     = "deny"
	ContainedEgressProxy    = "proxy"
	ContainedEgressDirectIP = "direct_ip"
)

const (
	BoundaryPermitUnixSocket    = "local_service.unix_socket"
	BoundaryPermitDirectIP      = "network.direct_ip"
	BoundaryPermitLoopback      = "network.loopback_connect"
	BoundaryPermitDeclaredHosts = "network.declared_hosts"
)

// BoundaryPermit identifies approved authority without exposing it.
type BoundaryPermit struct {
	Kind   string
	Digest string
	Count  int
}

// Contained is the applied per-action confinement fact.
type Contained struct {
	ProcessControl bool
	HostExecution  bool
	// FSJailed reports whether the write jail is active.
	FSJailed bool
	// Egress is empty when FSJailed is false.
	Egress string
	// Roots identify attached project and draft trees.
	Roots []string
	// WriteRoots includes roots, system scratch, and approved grants.
	WriteRoots []string
	// SocketPathsDigest identifies applied AF_UNIX grants.
	SocketPathsDigest string
	// SocketCount is the number of applied socket grants.
	SocketCount int
	// DirectIP is true when the action's confinement selects NetworkDirectIP.
	DirectIP bool
	// LoopbackAccess reports direct localhost authority.
	LoopbackAccess bool
	// LoopbackPortsDigest identifies a narrowed local destination-port grant.
	LoopbackPortsDigest string
	// DeclaredHostsDigest identifies host-derived destinations.
	DeclaredHostsDigest string
	// DeclaredHostCount is the number of hosts behind DeclaredHostsDigest.
	DeclaredHostCount int
	BoundaryPermits   []BoundaryPermit
}

// WithDialedSockets states sockets the host dials itself. They are authority
// whether or not a process boundary applies, so they never depend on one.
func (c Contained) WithDialedSockets(grants []confine.SocketGrant) Contained {
	if len(grants) == 0 || c.SocketCount > 0 {
		return c
	}
	c.SocketPathsDigest = confine.SocketPathsDigest(grants)
	c.SocketCount = len(grants)
	c.BoundaryPermits = append(append([]BoundaryPermit(nil), c.BoundaryPermits...), BoundaryPermit{
		Kind: BoundaryPermitUnixSocket, Digest: c.SocketPathsDigest, Count: c.SocketCount,
	})
	return c
}

// Active reports whether OS confine will contain this action.
func (c Contained) Active() bool {
	return c.FSJailed
}

// ActionConfineInputs describes one process boundary.
type ActionConfineInputs struct {
	ProcessControl bool
	HostExecution  bool
	ProjectID      string
	// Roots are host-derived write-jail roots.
	Roots []string
	// SessionScratchRoot is the session-owned scratch root.
	SessionScratchRoot string
	// OverlayWriteRoots contains chat-scoped write grants.
	OverlayWriteRoots []string
	PolicyWriteGrants []confine.ProtectedPathGrant
	// OverlayReadPaths contains chat-scoped protected read grants.
	OverlayReadPaths []string
	// ReadDenyPaths are host-selected read exclusions.
	ReadDenyPaths []string
	// ReadRoots are host-selected allow-backs below denied ancestors.
	ReadRoots        []string
	SocketGrants     []confine.SocketGrant
	SocksProxyEnv    bool
	DirectIP         bool
	DirectIPDeclared []string
	// LocalListen grants local listener authority; the egress mode is unchanged.
	LocalListen          bool
	LocalListenPorts     []uint16
	LoopbackConnect      bool
	LoopbackConnectPorts []uint16
}

// ActionConfineRequest builds the request shared by approval and execution.
func ActionConfineRequest(in ActionConfineInputs) confine.Request {
	overlayRoots, protected := confine.SplitChatOverlay(in.OverlayWriteRoots)
	reads := make([]confine.ProtectedPathGrant, 0, len(in.OverlayReadPaths))
	for _, p := range in.OverlayReadPaths {
		reads = append(reads, confine.NewProtectedPathGrant(p))
	}
	req := confine.Request{
		ProcessControl: in.ProcessControl, HostExecution: in.HostExecution,
		ProjectID:            in.ProjectID,
		Roots:                append([]string(nil), in.Roots...),
		SessionScratchRoot:   in.SessionScratchRoot,
		GrantedWriteRoots:    overlayRoots,
		SocketGrants:         append([]confine.SocketGrant(nil), in.SocketGrants...),
		ProtectedWriteGrants: protected,
		PolicyWriteGrants:    append([]confine.ProtectedPathGrant(nil), in.PolicyWriteGrants...),
		ProtectedReadGrants:  reads,
		ReadDenyPaths:        append([]string(nil), in.ReadDenyPaths...),
		ReadRoots:            append([]string(nil), in.ReadRoots...),
		SocksProxyEnv:        in.SocksProxyEnv,
	}
	if in.DirectIP {
		req.Egress = confine.EgressDirectIP
		req.DirectIPDeclared = append([]string(nil), in.DirectIPDeclared...)
		// Direct IP strips mediation env; SOCKS injection would be a no-op at best.
		req.SocksProxyEnv = false
	}
	if in.LocalListen {
		req.LocalListen = true
		req.LocalListenPorts = append([]uint16(nil), in.LocalListenPorts...)
	}
	if in.LoopbackConnect {
		req.LoopbackConnect = true
		req.LoopbackConnectPorts = append([]uint16(nil), in.LoopbackConnectPorts...)
	}
	return req
}

// ContainedForAction projects the action's one confine.Request onto Contained.
func ContainedForAction(in ActionConfineInputs) Contained {
	return ContainedForRequest(ActionConfineRequest(in))
}

// ContainedForRequest derives the gate fact from applied confinement.
func ContainedForRequest(req confine.Request) Contained {
	return ContainedFromConfinement(confine.DefaultConfinement(req))
}

// ContainedFromConfinement reuses an existing confinement result.
func ContainedFromConfinement(c *confine.Confinement, ok bool) Contained {
	if c != nil && c.HostExecution {
		return Contained{HostExecution: true, Roots: append([]string(nil), c.Roots...), BoundaryPermits: []BoundaryPermit{{Kind: "execution.host", Digest: "enabled", Count: 1}}}
	}
	if !ok || c == nil {
		return Contained{}
	}
	contained := Contained{
		ProcessControl:    c.ProcessControl,
		FSJailed:          true,
		Egress:            ContainedEgressLabel(c.Network),
		Roots:             append([]string(nil), c.Roots...),
		WriteRoots:        confine.WriteRootsForBoundary(c.ProjectID, c.Roots, c.GrantedWriteRoots, c.SessionScratchRoot),
		SocketPathsDigest: confine.SocketPathsDigest(c.SocketGrants),
		SocketCount:       len(c.SocketGrants),
		DirectIP:          c.Network == confine.NetworkDirectIP,
		LoopbackAccess:    c.Network == confine.NetworkDirectIP || c.LoopbackConnect,
	}
	if c.LoopbackConnect && c.Network != confine.NetworkDirectIP {
		contained.LoopbackPortsDigest = portSetDigest(c.LoopbackConnectPorts)
		contained.BoundaryPermits = append(contained.BoundaryPermits, BoundaryPermit{
			Kind: BoundaryPermitLoopback, Digest: contained.LoopbackPortsDigest, Count: 1,
		})
	}
	if c.ProcessControl {
		contained.BoundaryPermits = append(contained.BoundaryPermits, BoundaryPermit{Kind: "process.signal", Digest: "enabled", Count: 1})
	}
	if contained.SocketCount > 0 {
		contained.BoundaryPermits = append(contained.BoundaryPermits, BoundaryPermit{
			Kind: BoundaryPermitUnixSocket, Digest: contained.SocketPathsDigest, Count: contained.SocketCount,
		})
	}
	if contained.DirectIP {
		// The digest distinguishes narrowed direct-IP authority.
		permit := BoundaryPermit{Kind: BoundaryPermitDirectIP, Digest: "enabled", Count: 1}
		if len(c.DirectIPPermits) > 0 {
			permit.Digest = confine.DirectIPPermitsDigest(c.DirectIPPermits)
			permit.Count = len(c.DirectIPPermits)
		}
		contained.BoundaryPermits = append(contained.BoundaryPermits, permit)
	}
	return contained
}

// EffectiveBoundaryPermits projects current substrate-specific facts when a
// caller has not supplied an explicit generalized permit list.
func (c Contained) EffectiveBoundaryPermits() []BoundaryPermit {
	if len(c.BoundaryPermits) > 0 {
		return append([]BoundaryPermit(nil), c.BoundaryPermits...)
	}
	var permits []BoundaryPermit
	if c.ProcessControl {
		permits = append(permits, BoundaryPermit{Kind: "process.signal", Digest: "enabled", Count: 1})
	}
	if c.HostExecution {
		permits = append(permits, BoundaryPermit{Kind: "execution.host", Digest: "enabled", Count: 1})
	}
	if c.SocketCount > 0 && strings.TrimSpace(c.SocketPathsDigest) != "" {
		permits = append(permits, BoundaryPermit{
			Kind: BoundaryPermitUnixSocket, Digest: c.SocketPathsDigest, Count: c.SocketCount,
		})
	}
	if c.DirectIP {
		permits = append(permits, BoundaryPermit{
			Kind: BoundaryPermitDirectIP, Digest: "enabled", Count: 1,
		})
	}
	if c.LoopbackAccess && !c.DirectIP {
		digest := strings.TrimSpace(c.LoopbackPortsDigest)
		if digest == "" {
			digest = "any"
		}
		permits = append(permits, BoundaryPermit{Kind: BoundaryPermitLoopback, Digest: digest, Count: 1})
	}
	if c.DeclaredHostCount > 0 && strings.TrimSpace(c.DeclaredHostsDigest) != "" {
		permits = append(permits, BoundaryPermit{
			Kind: BoundaryPermitDeclaredHosts, Digest: c.DeclaredHostsDigest, Count: c.DeclaredHostCount,
		})
	}
	return permits
}

func portSetDigest(ports []uint16) string {
	if len(ports) == 0 {
		return "any"
	}
	values := append([]uint16(nil), ports...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	parts := make([]string, 0, len(values))
	for _, port := range values {
		parts = append(parts, strconv.Itoa(int(port)))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ContainedEgressLabel maps a confine.NetworkMode onto Contained.Egress.
func ContainedEgressLabel(mode confine.NetworkMode) string {
	switch mode {
	case confine.NetworkDeny:
		return ContainedEgressDeny
	case confine.NetworkProxyOnly:
		return ContainedEgressProxy
	case confine.NetworkDirectIP:
		return ContainedEgressDirectIP
	default:
		return ContainedEgressDeny
	}
}
