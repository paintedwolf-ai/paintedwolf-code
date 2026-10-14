//go:build linux

package linuxsandbox

import (
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
	handoffProxy = "proxy"
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
// inner helper hands it from the host's network namespace.
type supervisor struct {
	control *os.File
	wg      sync.WaitGroup
	mu      sync.Mutex
	closers []io.Closer
}

func newSupervisor(control *os.File) *supervisor {
	return &supervisor{control: control}
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
		fd, ok := receivedFD(oob[:oobn])
		if !ok {
			continue
		}
		var h handoff
		if json.Unmarshal(header[:n], &h) != nil {
			_ = unix.Close(fd)
			continue
		}
		s.accept(h, fd)
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
	switch h.Kind {
	case handoffProxy:
		ap, err := netip.ParseAddrPort(h.Addr)
		if err != nil || !ap.Addr().IsLoopback() {
			_ = unix.Close(fd)
			return
		}
		f := os.NewFile(uintptr(fd), "sandbox-listener")
		ln, err := net.FileListener(f)
		_ = f.Close()
		if err != nil {
			return
		}
		s.track(ln)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			forwardListener(ln, ap)
		}()
	default:
		_ = unix.Close(fd)
	}
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
			outside, err := net.Dial("tcp", upstream.String())
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
