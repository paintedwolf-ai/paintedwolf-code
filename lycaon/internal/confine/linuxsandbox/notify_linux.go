//go:build linux

package linuxsandbox

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// seccompNotif mirrors struct seccomp_notif.
type seccompNotif struct {
	ID    uint64
	PID   uint32
	Flags uint32
	Nr    int32
	Arch  uint32
	IP    uint64
	Args  [6]uint64
}

// seccompNotifResp mirrors struct seccomp_notif_resp.
type seccompNotifResp struct {
	ID    uint64
	Val   int64
	Error int32
	Flags uint32
}

// seccompNotifAddfd mirrors struct seccomp_notif_addfd.
type seccompNotifAddfd struct {
	ID         uint64
	Flags      uint32
	SrcFD      uint32
	NewFD      uint32
	NewFDFlags uint32
}

// maxSockaddr bounds the address the supervisor copies from the target.
const maxSockaddr = 128

// serveNotifications answers the sandbox's notified calls until the listener
// closes, which happens when the last sandboxed process exits.
func (s *supervisor) serveNotifications(listener int) {
	defer func() { _ = unix.Close(listener) }()
	for {
		var n seccompNotif
		if err := ioctlPtr(listener, unix.SECCOMP_IOCTL_NOTIF_RECV, unsafe.Pointer(&n)); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return
		}
		go s.answer(listener, n)
	}
}

// answer performs one notified call and replies with its result. Any failure
// to read the call refuses it: a call is never run unexamined.
func (s *supervisor) answer(listener int, n seccompNotif) {
	result, errno, examined := s.perform(listener, n)
	if !examined {
		s.report(Refusal{Unexamined: true})
	}
	resp := seccompNotifResp{ID: n.ID, Val: result}
	if errno != 0 {
		resp = seccompNotifResp{ID: n.ID, Error: -int32(errno)}
	}
	_ = ioctlPtr(listener, unix.SECCOMP_IOCTL_NOTIF_SEND, unsafe.Pointer(&resp))
}

// perform returns the call's result or the errno to answer with, and whether
// the supervisor could examine the call at all. A call that ended before it
// was examined is not reported.
func (s *supervisor) perform(listener int, n seccompNotif) (int64, unix.Errno, bool) {
	target, err := openTarget(listener, n)
	if err != nil {
		return 0, unix.EACCES, errors.Is(err, unix.ESRCH)
	}
	defer target.close()
	fd := int(int32(n.Args[0]))
	sock, err := unix.PidfdGetfd(target.pidfd, fd, 0)
	if err != nil {
		// A descriptor that is not open is the caller's own error.
		return 0, errnoOf(err, unix.EBADF), errors.Is(err, unix.EBADF)
	}
	defer func() { _ = unix.Close(sock) }()
	info, err := describeSocket(sock)
	if err != nil {
		return 0, errnoOf(err, unix.ENOTSOCK), true
	}
	result, errno := s.decideAndPerform(target, n, fd, sock, info)
	return result, errno, true
}

func (s *supervisor) decideAndPerform(target *targetProcess, n seccompNotif, fd, sock int, info socketInfo) (int64, unix.Errno) {
	req := SocketRequest{Stream: info.typ == unix.SOCK_STREAM}
	cookie, _ := unix.GetsockoptUint64(sock, unix.SOL_SOCKET, unix.SO_COOKIE)
	req.HostSocket, req.HostListener = s.hostSocket(cookie)

	var raw []byte
	switch n.Nr {
	case unix.SYS_LISTEN:
		req.Op = OpListen
	case unix.SYS_CONNECT, unix.SYS_BIND:
		req.Op = OpConnect
		if n.Nr == unix.SYS_BIND {
			req.Op = OpBind
		}
		size := int(n.Args[2])
		if size < 0 || size > maxSockaddr {
			return 0, unix.EINVAL
		}
		raw = make([]byte, size)
		if err := target.read(n.Args[1], raw); err != nil {
			return 0, unix.EFAULT
		}
	default:
		return 0, unix.ENOSYS
	}
	if req.Op != OpListen {
		if err := s.decodeAddress(&req, info, raw, target); err != nil {
			return 0, unix.EINVAL
		}
	}
	if !target.valid() {
		return 0, unix.EINTR
	}
	decision := s.network.Decide(req)
	if decision.Refusal != nil {
		decision.Refusal.Process = target.comm()
		s.report(*decision.Refusal)
	}
	switch decision.Action {
	case Refuse:
		return 0, unix.EACCES
	case HostSocket:
		return s.hostSocketCall(target, fd, sock, info, req, decision)
	}
	switch req.Op {
	case OpListen:
		return rawResult(unix.Syscall(unix.SYS_LISTEN, uintptr(sock), uintptr(n.Args[1]), 0))
	case OpBind:
		return rawResult(unix.Syscall(unix.SYS_BIND, uintptr(sock), uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw))))
	default:
		if req.Unix != "" {
			// The resolved path is what the grant names and what is connected.
			sa := &unix.SockaddrUnix{Name: req.Unix}
			return 0, errnoOf(unix.Connect(sock, sa), 0)
		}
		if len(raw) == 0 {
			return 0, unix.EINVAL
		}
		return rawResult(unix.Syscall(unix.SYS_CONNECT, uintptr(sock), uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw))))
	}
}

// decodeAddress fills the request's address from the supervisor's copy.
func (s *supervisor) decodeAddress(req *SocketRequest, info socketInfo, raw []byte, target *targetProcess) error {
	if len(raw) < 2 {
		return errors.New("short address")
	}
	family := binary.NativeEndian.Uint16(raw[:2])
	switch {
	case family == unix.AF_UNSPEC:
		return nil
	case family == unix.AF_UNIX:
		path := raw[2:]
		if len(path) == 0 || path[0] == 0 {
			req.Abstract = true
			return nil
		}
		if i := indexByte(path, 0); i >= 0 {
			path = path[:i]
		}
		req.Unix = resolveSocketPath(string(path), target)
		return nil
	case family == unix.AF_INET && len(raw) >= 8:
		port := binary.BigEndian.Uint16(raw[2:4])
		req.IP = netip.AddrPortFrom(netip.AddrFrom4([4]byte(raw[4:8])), port)
	case family == unix.AF_INET6 && len(raw) >= 24:
		port := binary.BigEndian.Uint16(raw[2:4])
		req.IP = netip.AddrPortFrom(netip.AddrFrom16([16]byte(raw[8:24])), port)
	default:
		// Other families stay inside the sandbox's namespaces.
		return nil
	}
	if req.Op == OpConnect && req.IP.Addr().Unmap().IsLoopback() && !req.HostSocket {
		req.SandboxListener = sandboxListens(target.pid, info.typ, req.IP.Port())
	}
	return nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// resolveSocketPath gives a path-socket address the meaning the target's
// working directory gives it, with symbolic links resolved, so a grant matches
// the socket it names whatever spelling reaches it.
func resolveSocketPath(path string, target *targetProcess) string {
	if !filepath.IsAbs(path) {
		cwd, err := os.Readlink("/proc/" + strconv.Itoa(target.pid) + "/cwd")
		if err != nil {
			return ""
		}
		path = filepath.Join(cwd, path)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// hostSocketCall runs an admitted call on a fresh socket in the host's
// namespace and, once it succeeds, puts that socket in the target's place.
func (s *supervisor) hostSocketCall(target *targetProcess, fd, sock int, info socketInfo, req SocketRequest, d Decision) (int64, unix.Errno) {
	host, err := unix.Socket(info.domain, info.typ|unix.SOCK_CLOEXEC, info.protocol)
	if err != nil {
		return 0, errnoOf(err, unix.EACCES)
	}
	defer func() { _ = unix.Close(host) }()
	copySocketOptions(sock, host)
	listener := false
	switch req.Op {
	case OpBind:
		family, sa := sockaddrOf(d.Bind)
		if family != info.domain {
			_, sa = sockaddrOf(netip.AddrPortFrom(netip.AddrFrom16(d.Bind.Addr().As16()), d.Bind.Port()))
		}
		if err := unix.Bind(host, sa); err != nil {
			return 0, errnoOf(err, unix.EACCES)
		}
		// Listening at once leaves no bound, unconnected host socket that a
		// later call could point somewhere else.
		if err := unix.Listen(host, unix.SOMAXCONN); err != nil {
			return 0, errnoOf(err, unix.EACCES)
		}
		listener = true
	default:
		_, sa := sockaddrOf(req.IP)
		if info.domain == unix.AF_INET6 && req.IP.Addr().Is4() {
			_, sa = sockaddrOf(netip.AddrPortFrom(netip.AddrFrom16(req.IP.Addr().As16()), req.IP.Port()))
		}
		if err := unix.Connect(host, sa); err != nil {
			return 0, errnoOf(err, unix.ECONNREFUSED)
		}
	}
	if info.nonblocking {
		_ = unix.SetNonblock(host, true)
	}
	cookie, err := unix.GetsockoptUint64(host, unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil {
		return 0, unix.EACCES
	}
	s.markHost(cookie, listener)
	add := seccompNotifAddfd{
		ID:         target.id,
		Flags:      unix.SECCOMP_ADDFD_FLAG_SETFD,
		SrcFD:      uint32(host),
		NewFD:      uint32(fd),
		NewFDFlags: target.fdFlags(fd),
	}
	if err := ioctlPtr(target.listener, unix.SECCOMP_IOCTL_NOTIF_ADDFD, unsafe.Pointer(&add)); err != nil {
		s.forgetHost(cookie)
		return 0, unix.EINTR
	}
	return 0, 0
}

// copySocketOptions carries the options a program commonly sets before
// connecting or binding to the socket that replaces its own.
func copySocketOptions(from, to int) {
	for _, opt := range []struct{ level, name int }{
		{unix.SOL_SOCKET, unix.SO_REUSEADDR},
		{unix.SOL_SOCKET, unix.SO_REUSEPORT},
		{unix.SOL_SOCKET, unix.SO_KEEPALIVE},
		{unix.IPPROTO_TCP, unix.TCP_NODELAY},
		{unix.IPPROTO_IPV6, unix.IPV6_V6ONLY},
	} {
		if v, err := unix.GetsockoptInt(from, opt.level, opt.name); err == nil {
			_ = unix.SetsockoptInt(to, opt.level, opt.name, v)
		}
	}
}

type socketInfo struct {
	domain, typ, protocol int
	nonblocking           bool
}

func describeSocket(fd int) (socketInfo, error) {
	var info socketInfo
	var err error
	if info.domain, err = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN); err != nil {
		return info, err
	}
	if info.typ, err = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE); err != nil {
		return info, err
	}
	if info.protocol, err = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PROTOCOL); err != nil {
		return info, err
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return info, err
	}
	info.nonblocking = flags&unix.O_NONBLOCK != 0
	return info, nil
}

// sandboxListens reports whether a socket in the target's network namespace
// listens on, or for datagrams is bound to, port.
func sandboxListens(pid, typ int, port uint16) bool {
	tables := []string{"tcp", "tcp6"}
	if typ != unix.SOCK_STREAM {
		tables = []string{"udp", "udp6"}
	}
	want := fmt.Sprintf(":%04X", port)
	for _, table := range tables {
		f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/net/" + table)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 || !strings.HasSuffix(fields[1], want) {
				continue
			}
			// 0A is TCP_LISTEN; a bound datagram socket shows 07.
			if (typ == unix.SOCK_STREAM && fields[3] == "0A") || (typ != unix.SOCK_STREAM && fields[3] == "07") {
				_ = f.Close()
				return true
			}
		}
		_ = f.Close()
	}
	return false
}

// targetProcess is the calling process of one notification, opened in a way
// that cannot later refer to a different process.
type targetProcess struct {
	listener int
	id       uint64
	pid      int
	pidfd    int
	mem      *os.File
}

func openTarget(listener int, n seccompNotif) (*targetProcess, error) {
	t := &targetProcess{listener: listener, id: n.ID, pid: int(n.PID), pidfd: -1}
	fd, err := unix.PidfdOpen(t.pid, 0)
	if err != nil {
		return nil, err
	}
	t.pidfd = fd
	mem, err := os.Open("/proc/" + strconv.Itoa(t.pid) + "/mem")
	if err != nil {
		t.close()
		return nil, err
	}
	t.mem = mem
	// Both handles were opened while the call was still pending, so they name
	// the process that made it, not a later one with its number.
	if !t.valid() {
		t.close()
		return nil, unix.ESRCH
	}
	return t, nil
}

func (t *targetProcess) valid() bool {
	id := t.id
	return ioctlPtr(t.listener, unix.SECCOMP_IOCTL_NOTIF_ID_VALID, unsafe.Pointer(&id)) == nil
}

func (t *targetProcess) read(addr uint64, into []byte) error {
	if len(into) == 0 {
		return nil
	}
	n, err := t.mem.ReadAt(into, int64(addr))
	if err != nil || n != len(into) {
		return errors.New("short read of the target's memory")
	}
	return nil
}

func (t *targetProcess) comm() string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(t.pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// fdFlags keeps close-on-exec on a descriptor the supervisor replaces.
func (t *targetProcess) fdFlags(fd int) uint32 {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(t.pid) + "/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		value, ok := strings.CutPrefix(line, "flags:")
		if !ok {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimSpace(value), 8, 32)
		if err == nil && flags&unix.O_CLOEXEC != 0 {
			return unix.O_CLOEXEC
		}
	}
	return 0
}

func (t *targetProcess) close() {
	if t.mem != nil {
		_ = t.mem.Close()
	}
	if t.pidfd >= 0 {
		_ = unix.Close(t.pidfd)
	}
}

func ioctlPtr(fd int, req uint, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func rawResult(r1, _ uintptr, errno unix.Errno) (int64, unix.Errno) {
	if errno != 0 {
		return 0, errno
	}
	return int64(r1), 0
}

// errnoOf extracts the errno to answer with; fallback when err carries none.
func errnoOf(err error, fallback unix.Errno) unix.Errno {
	if err == nil {
		return 0
	}
	var errno unix.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return fallback
}
