//go:build linux

package linuxsandbox

import (
	"io"
	"net"
	"net/netip"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// datagramRelays carries the sandbox's datagrams to the host's loopback
// services and a granted datagram listener's from the host. Each relay pairs a
// socket inside the sandbox, from the inner helper, with sockets of the outer
// helper's own on the host, so a confined process never holds a host socket:
// one could send anywhere a later call names.
type datagramRelays struct {
	network Network
	factory *socketFactory
	mu      sync.Mutex
	// toHost and fromHost hold the addresses already relayed: inside the
	// sandbox for host services, on the host for sandbox listeners.
	toHost   map[netip.AddrPort]bool
	fromHost map[netip.AddrPort]bool
	closers  []io.Closer
}

func newDatagramRelays(network Network, factory *socketFactory) *datagramRelays {
	return &datagramRelays{network: network, factory: factory, toHost: map[netip.AddrPort]bool{}, fromHost: map[netip.AddrPort]bool{}}
}

// openLaunchRelays relays each loopback datagram service the host serves at
// launch and the network grants, so a process reaches it however it sends.
func (r *datagramRelays) openLaunchRelays() {
	if !r.network.LoopbackDatagrams() {
		return
	}
	portRange, _ := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	opened := 0
	for _, a := range loopbackServiceAddrs(hostDatagramSockets(), ephemeralFloor(string(portRange))) {
		if opened >= maxSnapshotRelays {
			return
		}
		if r.network.loopbackAllowed(a.Port()) && r.relayToHost(a) {
			opened++
		}
	}
}

// ensureToHost relays dst inside the sandbox to the host's service there, when
// the host serves it and no relay does yet.
func (r *datagramRelays) ensureToHost(dst netip.AddrPort) {
	dst = netip.AddrPortFrom(dst.Addr().Unmap(), dst.Port())
	r.mu.Lock()
	known := r.toHost[dst]
	r.mu.Unlock()
	if !known && hostServes(hostDatagramSockets(), dst) {
		r.relayToHost(dst)
	}
}

func (r *datagramRelays) relayToHost(dst netip.AddrPort) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.toHost[dst] {
		return true
	}
	fd, err := r.factory.socket(familyOf(dst))
	if err != nil {
		return false
	}
	_, sa := sockaddrOf(dst)
	// Taken inside the sandbox means a process there serves this address.
	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return false
	}
	pc, err := packetConnOf(fd)
	if err != nil {
		return false
	}
	r.toHost[dst] = true
	r.closers = append(r.closers, pc)
	go relayDatagrams(pc, func() (net.Conn, error) { return net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(dst)) })
	return true
}

// expose relays host, on the host, to a datagram listener the sandbox binds at
// inside. The host address is taken first: a port the host already uses
// refuses the bind, as it would outside the sandbox.
func (r *datagramRelays) expose(host, inside netip.AddrPort, bind func() (int64, unix.Errno)) (int64, unix.Errno) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fromHost[host] {
		return bind()
	}
	conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(host))
	if err != nil {
		return 0, errnoOf(err, unix.EADDRINUSE)
	}
	result, errno := bind()
	if errno != 0 {
		_ = conn.Close()
		return result, errno
	}
	target := inside
	if target.Addr().IsUnspecified() {
		target = netip.AddrPortFrom(loopbackOf(target.Addr()), target.Port())
	}
	r.fromHost[host] = true
	r.closers = append(r.closers, conn)
	go relayDatagrams(conn, func() (net.Conn, error) { return r.sandboxConn(target) })
	return result, 0
}

// sandboxConn is a socket inside the sandbox connected to target there.
func (r *datagramRelays) sandboxConn(target netip.AddrPort) (net.Conn, error) {
	fd, err := r.factory.socket(familyOf(target))
	if err != nil {
		return nil, err
	}
	_, sa := sockaddrOf(target)
	if err := unix.Connect(fd, sa); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "sandbox-datagram")
	defer func() { _ = f.Close() }()
	return net.FileConn(f)
}

func (r *datagramRelays) close() {
	r.mu.Lock()
	closers := r.closers
	r.closers = nil
	r.mu.Unlock()
	for _, c := range closers {
		_ = c.Close()
	}
}

func packetConnOf(fd int) (net.PacketConn, error) {
	f := os.NewFile(uintptr(fd), "sandbox-datagram")
	defer func() { _ = f.Close() }()
	return net.FilePacketConn(f)
}

func familyOf(a netip.AddrPort) int {
	if a.Addr().Is4() {
		return unix.AF_INET
	}
	return unix.AF_INET6
}

// hostDatagramSockets lists the host's bound datagram sockets. The outer
// helper runs in the host's network namespace, so its own tables are the host's.
func hostDatagramSockets() []netip.AddrPort {
	var out []netip.AddrPort
	for _, table := range []string{"/proc/self/net/udp", "/proc/self/net/udp6"} {
		raw, err := os.ReadFile(table)
		if err == nil {
			out = append(out, boundDatagramSockets(string(raw))...)
		}
	}
	return out
}
