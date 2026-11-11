//go:build darwin && cgo

package lineage

import (
	"errors"
	"net/netip"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// unreadableByThisUser requires the kernel's refusal and a different owner. A
// process of this user that cannot be read has no exclusion to rest on.
func unreadableByThisUser(pid int, err error) bool {
	if !errors.Is(err, syscall.EPERM) {
		return false
	}
	info, infoErr := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return infoErr == nil && info != nil && int(info.Proc.P_pid) == pid && info.Eproc.Ucred.Uid != uint32(os.Geteuid())
}

func socketWalker() func(pid int) ([]SocketObservation, error) { return SocketsOf }

// unreadHoldersExcluded rests on what the kernel admits to bind, measured on
// macOS 15.7 and 27.2. A second socket on an exact address needs SO_REUSEPORT on
// both, and connections reach the later one. A wildcard socket of another user
// can coexist, but connections to the exact address still reach the exact socket.
// The other loopback family is a separate address, so it is probed.
func unreadHoldersExcluded(endpoint netip.AddrPort, walk endpointHolders) bool {
	return bindExcludesOthers(endpoint, walk, loopbackAddressFree)
}

func bindExcludesOthers(endpoint netip.AddrPort, walk endpointHolders, free func(netip.AddrPort) bool) bool {
	for _, holder := range walk.holders {
		if holder.sharedBind {
			return false
		}
	}
	if len(walk.sibling) > 0 {
		return true
	}
	return free(netip.AddrPortFrom(siblingLoopback(endpoint.Addr()), endpoint.Port()))
}

// loopbackAddressFree binds without listening, so it never accepts a connection.
// The kernel refuses it for any holder of the address or its family's wildcard,
// whoever owns them, and for a recently closed connection, which only asks again.
func loopbackAddressFree(endpoint netip.AddrPort) bool {
	addr := endpoint.Addr().Unmap()
	var family int
	var sockaddr syscall.Sockaddr
	if addr.Is4() {
		family, sockaddr = syscall.AF_INET, &syscall.SockaddrInet4{Port: int(endpoint.Port()), Addr: addr.As4()}
	} else {
		family, sockaddr = syscall.AF_INET6, &syscall.SockaddrInet6{Port: int(endpoint.Port()), Addr: addr.As16()}
	}
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	if family == syscall.AF_INET6 {
		if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1); err != nil {
			return false
		}
	}
	err = syscall.Bind(fd, sockaddr)
	// An address this host does not configure can have no holder.
	return err == nil || errors.Is(err, syscall.EADDRNOTAVAIL)
}
