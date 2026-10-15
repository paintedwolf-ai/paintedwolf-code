//go:build linux

package linuxsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// controlFD is the inner helper's end of the control socket it shares with the
// outer helper. Descriptors created inside the sandbox's network namespace
// cross it to the outer helper, which serves them from the host's.
const controlFD = 6

// handoff heads each descriptor the inner helper passes over the control socket.
type handoff struct {
	Kind string `json:"kind"`
	// Addr is the address the descriptor listens on inside the sandbox; the
	// outer helper forwards to the same address on the host.
	Addr string `json:"addr,omitempty"`
}

// Handoff kinds.
const (
	handoffProxy  = "proxy"
	handoffNotify = "notify"
)

// maxHandoff bounds one control message's header.
const maxHandoff = 4096

// proxyAddrs lists the distinct mediated endpoints a plan carries.
func proxyAddrs(n Network) ([]netip.AddrPort, error) {
	var out []netip.AddrPort
	seen := map[netip.AddrPort]bool{}
	for _, raw := range []string{n.ProxyAddr, n.SocksProxyAddr} {
		if raw == "" {
			continue
		}
		ap, err := netip.ParseAddrPort(raw)
		if err != nil || !ap.Addr().IsLoopback() || ap.Port() == 0 {
			return nil, &Unsupported{Code: UnsupportedNetworkGrant, Detail: "mediated endpoint " + raw + " is not a loopback address and port"}
		}
		if !seen[ap] {
			seen[ap] = true
			out = append(out, ap)
		}
	}
	return out, nil
}

// handOffProxyListeners binds each mediated endpoint inside the sandbox's
// network namespace and passes the listener to the outer helper, which accepts
// there and forwards to the broker on the host. The inner helper keeps no copy.
func handOffProxyListeners(n Network) error {
	addrs, err := proxyAddrs(n)
	if err != nil {
		return err
	}
	for _, ap := range addrs {
		fd, err := listenTCP(ap)
		if err != nil {
			return fmt.Errorf("mediated endpoint %s: %w", ap, err)
		}
		err = sendHandoff(handoff{Kind: handoffProxy, Addr: ap.String()}, fd)
		_ = unix.Close(fd)
		if err != nil {
			return fmt.Errorf("hand off mediated endpoint %s: %w", ap, err)
		}
	}
	return nil
}

func listenTCP(ap netip.AddrPort) (int, error) {
	family, sa := sockaddrOf(ap)
	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if err := unix.Listen(fd, unix.SOMAXCONN); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func sockaddrOf(ap netip.AddrPort) (int, unix.Sockaddr) {
	if ap.Addr().Is4() {
		return unix.AF_INET, &unix.SockaddrInet4{Port: int(ap.Port()), Addr: ap.Addr().As4()}
	}
	return unix.AF_INET6, &unix.SockaddrInet6{Port: int(ap.Port()), Addr: ap.Addr().As16()}
}

func sendHandoff(h handoff, fd int) error {
	header, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return unix.Sendmsg(controlFD, header, unix.UnixRights(fd), nil, 0)
}

// supervisor is the outer helper's side of the sandbox: it serves what the
// inner helper hands it from the host's network namespace and decides the
// sandbox's socket calls.
type supervisor struct {
	control *os.File
	network Network
	factory *socketFactory
	relays  *datagramRelays
	host    *hostSockets
	wg      sync.WaitGroup
	mu      sync.Mutex
	closers []io.Closer
	// setupDone is set once the seccomp listener arrives, the last thing the
	// inner helper hands over before the target can run. Later handoffs are
	// refused: only a process that took over the inner helper could send one.
	setupDone bool
	// refusals reports each refused call to the action's feed.
	refusals *refusalLines
}

func newSupervisor(control *os.File, network Network, refusals io.Writer) *supervisor {
	factory := newSocketFactory(control)
	return &supervisor{
		control: control, network: network, factory: factory,
		relays: newDatagramRelays(network, factory), host: newHostSockets(),
		refusals: &refusalLines{w: refusals},
	}
}

// serve reads handoffs until the inner helper closes its end.
func (s *supervisor) serve() {
	header := make([]byte, maxHandoff)
	oob := make([]byte, unix.CmsgSpace(4))
	for {
		n, oobn, _, _, err := unix.Recvmsg(int(s.control.Fd()), header, oob, unix.MSG_CMSG_CLOEXEC)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return
		}
		if n == 0 && oobn == 0 {
			return
		}
		var h handoff
		decoded := json.Unmarshal(header[:n], &h) == nil
		fd, ok := receivedFD(oob[:oobn])
		switch {
		case decoded && h.Kind == handoffNoSocket:
			s.factory.deliver(-1)
		case !ok:
		case !decoded:
			_ = unix.Close(fd)
		case h.Kind == handoffSocket:
			s.factory.deliver(fd)
		default:
			s.accept(h, fd)
		}
	}
}

func receivedFD(oob []byte) (int, bool) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil || len(msgs) != 1 {
		return -1, false
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		return -1, false
	}
	return fds[0], true
}

func (s *supervisor) accept(h handoff, fd int) {
	if s.setupDone {
		_ = unix.Close(fd)
		return
	}
	switch h.Kind {
	case handoffProxy:
		ap, err := netip.ParseAddrPort(h.Addr)
		if err != nil || !ap.Addr().IsLoopback() {
			_ = unix.Close(fd)
			return
		}
		if ln, err := listenerOf(fd); err == nil {
			s.run(ln, func() { forwardListener(ln, ap) })
		}
	case handoffResolverUDP, handoffResolverTCP:
		upstream, ok := s.resolver()
		if !ok {
			_ = unix.Close(fd)
			return
		}
		if h.Kind == handoffResolverTCP {
			if ln, err := listenerOf(fd); err == nil {
				s.run(ln, func() { forwardListener(ln, upstream) })
			}
		} else if pc, err := packetConnOf(fd); err == nil {
			s.run(pc, func() { relayResolverDatagrams(pc, upstream) })
		}
	case handoffGateway:
		_ = unix.SetNonblock(fd, true)
		tun := os.NewFile(uintptr(fd), "sandbox-gateway")
		s.run(tun, func() { serveGateway(tun, s.network, s.refusals) })
	case handoffNotify:
		s.setupDone = true
		s.run(nil, func() { s.serveNotifications(fd) })
		// The inner helper answers socket requests until it is told the
		// launch relays are in place, and starts the target only then.
		go func() {
			s.relays.openLaunchRelays()
			s.factory.ready()
		}()
	default:
		_ = unix.Close(fd)
	}
}

// run serves fn until it returns; c, when set, closes with the supervisor.
func (s *supervisor) run(c io.Closer, fn func()) {
	if c != nil {
		s.track(c)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

func listenerOf(fd int) (net.Listener, error) {
	f := os.NewFile(uintptr(fd), "sandbox-listener")
	defer func() { _ = f.Close() }()
	return net.FileListener(f)
}

// resolver is the first resolver the plan names, the one the stub relays to.
func (s *supervisor) resolver() (netip.AddrPort, bool) {
	for _, raw := range s.network.Resolvers {
		if ap, err := netip.ParseAddrPort(raw); err == nil {
			return ap, true
		}
	}
	return netip.AddrPort{}, false
}

func (s *supervisor) track(c io.Closer) {
	s.mu.Lock()
	s.closers = append(s.closers, c)
	s.mu.Unlock()
}

// close ends every listener; connections in flight end with the process.
func (s *supervisor) close() {
	s.mu.Lock()
	closers := s.closers
	s.closers = nil
	s.mu.Unlock()
	for _, c := range closers {
		_ = c.Close()
	}
	s.relays.close()
	_ = s.control.Close()
}

// forwardListener accepts inside the sandbox and dials the same address on the
// host. When the host end refuses, the sandboxed client sees its connection
// close: an ended broker is never replaced by a direct route.
func forwardListener(ln net.Listener, upstream netip.AddrPort) {
	for {
		inside, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			outside, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", upstream.String())
			if err != nil {
				_ = inside.Close()
				return
			}
			splice(inside, outside)
		}()
	}
}

// splice copies both ways and half-closes each side as its peer finishes.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go pipe(a, b)
	go pipe(b, a)
	<-done
	<-done
	_ = a.Close()
	_ = b.Close()
}
