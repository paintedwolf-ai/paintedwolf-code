// Package linuxsandbox applies one command's boundary plan inside Linux
// namespaces with Landlock and seccomp, attests the kernel facts before the
// target runs, and relays signals and exit status for it.
package linuxsandbox

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// PlanVersion is the plan shape this helper reads.
const PlanVersion = 3

// Plan is the typed boundary the helper applies. The host renders it from the
// same policy inputs as the macOS profile; the helper never widens it.
type Plan struct {
	Version int `json:"version"`
	// Probe exits after attestation instead of running the target.
	Probe bool `json:"probe,omitempty"`
	// ProcessControl records the approved capability. Inside a PID namespace
	// it grants nothing beyond the namespace's own reach.
	ProcessControl bool       `json:"process_control,omitempty"`
	Attest         Attest     `json:"attest"`
	Filesystem     Filesystem `json:"filesystem"`
	Network        Network    `json:"network"`
}

// Attest carries the facts attestation compares against.
type Attest struct {
	// HostNamespaces are the engine's namespace identities by kind. The helper
	// must find every one of them different before it runs the target.
	HostNamespaces map[string]string `json:"host_namespaces"`
	// Omit names one step the helper leaves unapplied so attestation can be
	// exercised. Only tests set it; the helper refuses to run the target.
	Omit string `json:"omit,omitempty"`
}

// Namespace kinds attestation compares, named as /proc/self/ns lists them.
const (
	NamespaceUser  = "user"
	NamespaceMount = "mnt"
	NamespacePID   = "pid"
	NamespaceNet   = "net"
	NamespaceIPC   = "ipc"
)

// NamespaceKinds lists every namespace the helper creates.
func NamespaceKinds() []string {
	return []string{NamespaceUser, NamespaceMount, NamespacePID, NamespaceNet, NamespaceIPC}
}

// Steps Attest.Omit may name.
const (
	OmitLandlock = "landlock"
	OmitSeccomp  = "seccomp"
)

// Filesystem is the mount and Landlock expression of the filesystem rules.
// Landlock grants are positive; mounts express the denials inside them.
type Filesystem struct {
	// WriteRoots are directories the process may read and change.
	WriteRoots []string `json:"write_roots"`
	// WriteFiles are exact files the process may read and change.
	WriteFiles []string `json:"write_files"`
	// ReadOnly are paths below a write grant the process may read but not change.
	ReadOnly []string `json:"read_only"`
	// Hidden are paths replaced by an empty read-only placeholder.
	Hidden []string `json:"hidden"`
	// Restored re-exposes real paths below a hidden or read-only ancestor. The
	// Landlock rules decide what the process may do there.
	Restored []string `json:"restored"`
	// ReadOnlyNames are basenames protected at any depth below a directory,
	// including entries created after launch.
	ReadOnlyNames []NamedFloor `json:"read_only_names,omitempty"`
}

// Network modes, matching the confinement's network mode names.
const (
	NetworkDeny      = "deny"
	NetworkProxyOnly = "proxy_only"
	NetworkDirectIP  = "direct_ip"
)

// Network is the egress expression. Every mode runs in a network namespace
// holding one loopback interface. ProxyOnly adds the mediated endpoints there,
// served from the host by the outer helper. The supervisor admits the other
// grants call by call: loopback connects, listeners, direct IP, and exact
// Unix-socket paths reach the host only through sockets it creates or
// connects itself. Datagrams never leave the namespace on a socket the
// process holds: the outer helper relays them to the host's loopback services
// and carries them to other hosts through a gateway interface, deciding each
// datagram itself.
type Network struct {
	Mode                 string           `json:"mode"`
	LoopbackConnect      bool             `json:"loopback_connect,omitempty"`
	LoopbackConnectPorts []uint16         `json:"loopback_connect_ports,omitempty"`
	Listen               bool             `json:"listen,omitempty"`
	ListenPorts          []uint16         `json:"listen_ports,omitempty"`
	ProxyAddr            string           `json:"proxy_addr,omitempty"`
	SocksProxyAddr       string           `json:"socks_proxy_addr,omitempty"`
	DirectIPPermits      []DirectIPPermit `json:"direct_ip_permits,omitempty"`
	// SocketGrants connect through retained descriptors owned by the outer helper.
	SocketGrants []SocketGrant `json:"socket_grants,omitempty"`
	// beforeSocketConnect is an in-process synchronization seam; it is never
	// read from a plan or configuration and carries no authorization.
	beforeSocketConnect func() `json:"-"`
	// Resolvers are the host's resolvers, in order, that direct IP reaches
	// names through.
	Resolvers []string `json:"resolvers,omitempty"`
	Browser   bool     `json:"browser,omitempty"`
}

// SocketReferenceFD is the first inherited destination descriptor. The earlier
// slots carry the profile, lineage marker, refusal report and inner control.
const SocketReferenceFD = 7

// SocketGrant associates an approved selector with its retained destination.
type SocketGrant struct {
	Path string `json:"path"`
	FD   int    `json:"fd"`
	// Delegated is set when the retained object is a delegated service's
	// endpoint socket, however the grant named it.
	Delegated DelegatedService `json:"delegated,omitempty"`
}

// DirectIPPermit narrows direct IP by transport and port.
type DirectIPPermit struct {
	Protocol string `json:"protocol"`
	Port     uint16 `json:"port"`
}

// DatagramGateway reports whether datagrams may leave for other hosts, which
// they do only through the gateway the outer helper serves.
func (n Network) DatagramGateway() bool {
	if n.Mode != NetworkDirectIP {
		return false
	}
	if len(n.DirectIPPermits) == 0 {
		return true
	}
	for _, p := range n.DirectIPPermits {
		if p.Protocol == "udp" {
			return true
		}
	}
	return false
}

// LoopbackDatagrams reports whether datagrams may reach the host's loopback
// services, which they do through relays the outer helper serves.
func (n Network) LoopbackDatagrams() bool {
	return n.Mode == NetworkDirectIP || n.LoopbackConnect
}

// Unsupported is a plan this helper cannot express. The launch fails closed
// and names what it could not apply.
type Unsupported struct {
	Code   string
	Detail string
}

// Codes an Unsupported refusal carries.
const (
	UnsupportedPlanVersion    = "plan_version"
	UnsupportedNetworkMode    = "network_mode"
	UnsupportedNetworkGrant   = "network_grant"
	UnsupportedFilesystemRule = "filesystem_rule"
)

func (u *Unsupported) Error() string {
	if u.Detail == "" {
		return "linux sandbox cannot express this boundary: " + u.Code
	}
	return "linux sandbox cannot express this boundary: " + u.Code + ": " + u.Detail
}

// Encode renders the plan for the inherited pipe.
func (p Plan) Encode() ([]byte, error) {
	p.Filesystem = p.Filesystem.sorted()
	return json.Marshal(p)
}

// DecodePlan reads a plan the host rendered and checks its version.
func DecodePlan(raw []byte) (Plan, error) {
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return Plan{}, fmt.Errorf("decode boundary plan: %w", err)
	}
	if p.Version != PlanVersion {
		return Plan{}, &Unsupported{Code: UnsupportedPlanVersion, Detail: fmt.Sprintf("version %d, helper reads %d", p.Version, PlanVersion)}
	}
	return p, nil
}

// Validate reports the first part of the plan this helper cannot express.
func (p Plan) Validate() error {
	if p.Version != PlanVersion {
		return &Unsupported{Code: UnsupportedPlanVersion, Detail: fmt.Sprintf("version %d, helper reads %d", p.Version, PlanVersion)}
	}
	n := p.Network
	switch n.Mode {
	case NetworkDeny:
	case NetworkProxyOnly:
		if n.ProxyAddr == "" && n.SocksProxyAddr == "" {
			return &Unsupported{Code: UnsupportedNetworkGrant, Detail: "proxy_only without a mediated endpoint"}
		}
	case NetworkDirectIP:
		for _, permit := range n.DirectIPPermits {
			if permit.Protocol != "tcp" && permit.Protocol != "udp" {
				return &Unsupported{Code: UnsupportedNetworkGrant, Detail: "direct_ip " + permit.Protocol + " permit"}
			}
		}
	default:
		return &Unsupported{Code: UnsupportedNetworkMode, Detail: n.Mode}
	}
	for _, named := range p.Filesystem.ReadOnlyNames {
		if !filepath.IsAbs(named.Below) || named.Name == "" || strings.ContainsRune(named.Name, '/') {
			return &Unsupported{Code: UnsupportedFilesystemRule, Detail: "protected name " + named.Name + " below " + named.Below}
		}
	}
	if len(n.SocketGrants) > 8 {
		return &Unsupported{Code: UnsupportedNetworkGrant, Detail: "too many socket references"}
	}
	seenSockets := make(map[string]bool)
	seenFDs := make(map[int]bool)
	for _, grant := range n.SocketGrants {
		if !filepath.IsAbs(grant.Path) || strings.ContainsRune(grant.Path, 0) || grant.FD < SocketReferenceFD || grant.FD >= SocketReferenceFD+8 || seenSockets[grant.Path] || seenFDs[grant.FD] {
			return &Unsupported{Code: UnsupportedNetworkGrant, Detail: "invalid or repeated socket reference"}
		}
		if grant.Delegated != "" && grant.Delegated != DelegatedNixDaemon {
			return &Unsupported{Code: UnsupportedNetworkGrant, Detail: "unknown delegated service " + string(grant.Delegated)}
		}
		seenSockets[grant.Path], seenFDs[grant.FD] = true, true
	}
	return nil
}

// sorted orders every list so plans compare and apply deterministically.
func (f Filesystem) sorted() Filesystem {
	for _, list := range []*[]string{&f.WriteRoots, &f.WriteFiles, &f.ReadOnly, &f.Hidden, &f.Restored} {
		*list = dedupeSorted(*list)
	}
	sort.Slice(f.ReadOnlyNames, func(i, j int) bool {
		a, b := f.ReadOnlyNames[i], f.ReadOnlyNames[j]
		return a.Below < b.Below || (a.Below == b.Below && a.Name < b.Name)
	})
	return f
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
