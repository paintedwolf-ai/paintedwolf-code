package linuxsandbox

import (
	"net/netip"
	"path/filepath"
	"slices"
)

// SocketOp is a socket syscall the supervisor decides and performs.
type SocketOp string

const (
	OpConnect SocketOp = "connect"
	OpBind    SocketOp = "bind"
	OpListen  SocketOp = "listen"
)

// SocketRequest is what the supervisor read about one notified syscall: the
// socket as it was when the supervisor took its own descriptor for it, and
// the address from the supervisor's own copy. The supervisor performs the call
// on that descriptor with that copy, so nothing the target changes afterwards
// reaches the kernel.
type SocketRequest struct {
	Op SocketOp
	// Unix is a path-socket address, already resolved to its real path.
	Unix string
	// Abstract marks an abstract or unnamed Unix-domain address.
	Abstract bool
	// IP is an internet address; zero when the call named none.
	IP netip.AddrPort
	// Stream is true for SOCK_STREAM.
	Stream bool
	// HostSocket reports that the socket lives in the host's network namespace
	// because the supervisor put it there.
	HostSocket bool
	// HostListener reports a host socket the supervisor created for a granted
	// listener.
	HostListener bool
	// SandboxListener reports that a socket inside the sandbox listens on the
	// destination port.
	SandboxListener bool
}

// Action is what the supervisor does with a notified syscall.
type Action int

const (
	// Refuse answers EACCES without running the call.
	Refuse Action = iota
	// Perform runs the call on the supervisor's descriptor for the target's
	// socket with the supervisor's copy of the address.
	Perform
	// HostSocket runs the call on a fresh socket in the host's namespace and,
	// when it succeeds, puts that socket in place of the target's. Only stream
	// sockets go there, connected or listening, so none can be pointed at a
	// new destination afterwards.
	HostSocket
)

// Decision is the supervisor's verdict and, when the operation will not reach
// its destination, the refusal to report.
type Decision struct {
	Action Action
	// Bind is the host address a granted listener binds.
	Bind netip.AddrPort
	// Refusal is set when the operation will not reach its destination.
	Refusal *Refusal
}

// Refusal is one refused socket operation as the supervisor reports it, in
// the vocabulary the macOS kernel uses for the same operation.
type Refusal struct {
	Operation string `json:"operation"`
	Target    string `json:"target,omitempty"`
	Process   string `json:"process,omitempty"`
}

// Decide applies the plan's network grants to one socket request.
func (n Network) Decide(r SocketRequest) Decision {
	switch {
	case r.Op == OpListen:
		return n.decideListen(r)
	case r.Unix != "" || r.Abstract:
		return n.decideUnix(r)
	case !r.IP.IsValid():
		// An unspecified address dissolves an association; it reaches nothing.
		return Decision{Action: Perform}
	case r.Op == OpBind:
		return n.decideBind(r)
	default:
		return n.decideOutbound(r)
	}
}

func (n Network) decideUnix(r SocketRequest) Decision {
	switch {
	case r.Abstract:
		// Abstract names belong to the sandbox's own network namespace.
		return Decision{Action: Perform}
	case r.Op == OpBind:
		return refuse("network-bind", r.Unix)
	case slices.Contains(n.SocketPaths, filepath.Clean(r.Unix)):
		return Decision{Action: Perform}
	default:
		return refuse("network-outbound", r.Unix)
	}
}

func (n Network) decideOutbound(r SocketRequest) Decision {
	ip := r.IP.Addr().Unmap()
	target := "remote:" + netip.AddrPortFrom(ip, r.IP.Port()).String()
	admitted := false
	if ip.IsLoopback() {
		if r.SandboxListener && !r.HostSocket {
			return Decision{Action: Perform}
		}
		admitted = n.loopbackAllowed(r.IP.Port())
	} else {
		admitted = n.directAllowed(r.Stream, r.IP.Port())
	}
	switch {
	case admitted && r.Stream:
		return Decision{Action: HostSocket}
	case r.HostSocket:
		return refuse("network-outbound", target)
	default:
		// The namespace has no route or listener for it and answers itself; a
		// datagram socket never leaves the sandbox.
		return Decision{Action: Perform, Refusal: &Refusal{Operation: "network-outbound", Target: target}}
	}
}

func (n Network) decideBind(r SocketRequest) Decision {
	ip := r.IP.Addr().Unmap()
	local := "local:" + netip.AddrPortFrom(ip, r.IP.Port()).String()
	if r.HostSocket {
		return refuse("network-bind", local)
	}
	if (!ip.IsLoopback() && !ip.IsUnspecified()) || !r.Stream || !n.listenAllowed(r.IP.Port()) {
		// The listener stays inside the sandbox, where only the command's own
		// processes reach it.
		return Decision{Action: Perform}
	}
	// A granted listener is reachable from this machine only.
	bind := netip.AddrPortFrom(ip, r.IP.Port())
	if ip.IsUnspecified() {
		bind = netip.AddrPortFrom(loopbackOf(r.IP.Addr()), r.IP.Port())
	}
	return Decision{Action: HostSocket, Bind: bind}
}

func (n Network) decideListen(r SocketRequest) Decision {
	if r.HostSocket && !r.HostListener {
		return refuse("network-bind", "")
	}
	return Decision{Action: Perform}
}

func loopbackOf(a netip.Addr) netip.Addr {
	if a.Is4() || a.Is4In6() {
		return netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	return netip.IPv6Loopback()
}

func (n Network) loopbackAllowed(port uint16) bool {
	if n.Mode == NetworkDirectIP {
		return true
	}
	return n.LoopbackConnect && (len(n.LoopbackConnectPorts) == 0 || slices.Contains(n.LoopbackConnectPorts, port))
}

func (n Network) listenAllowed(port uint16) bool {
	if n.Mode == NetworkDirectIP || n.Browser {
		return true
	}
	if !n.Listen {
		return false
	}
	if len(n.ListenPorts) == 0 {
		return true
	}
	return port != 0 && slices.Contains(n.ListenPorts, port)
}

func (n Network) directAllowed(stream bool, port uint16) bool {
	if n.Mode != NetworkDirectIP {
		return false
	}
	if len(n.DirectIPPermits) == 0 {
		return true
	}
	protocol := "udp"
	if stream {
		protocol = "tcp"
	}
	for _, p := range n.DirectIPPermits {
		if p.Protocol == protocol && p.Port == port {
			return true
		}
	}
	return false
}

func refuse(operation, target string) Decision {
	return Decision{Action: Refuse, Refusal: &Refusal{Operation: operation, Target: target}}
}
