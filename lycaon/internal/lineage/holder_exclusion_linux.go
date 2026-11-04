//go:build linux

package lineage

import (
	"errors"
	"io/fs"
	"net/netip"
)

// /proc/<pid>/fd is unreadable across users; /proc/net never is.
func unreadableByThisUser(_ int, err error) bool { return errors.Is(err, fs.ErrPermission) }

// socketWalker reads the socket table once, since one walk visits every process.
func socketWalker() func(pid int) ([]SocketObservation, error) {
	table, err := hostProc.sockets([]Protocol{ProtocolTCP})
	return func(pid int) ([]SocketObservation, error) {
		if err != nil {
			return nil, err
		}
		return hostProc.socketsOf(pid, table)
	}
}

func unreadHoldersExcluded(endpoint netip.AddrPort, walk endpointHolders) bool {
	table, err := hostProc.sockets([]Protocol{ProtocolTCP})
	return err == nil && tableExcludesOthers(table, endpoint, walk)
}

// tableExcludesOthers requires every listener a port-scoped grant reaches to be
// one the walk attributed. The table lists a socket whoever holds it, so a socket
// only an unread process holds appears here without an attributed holder.
func tableExcludesOthers(table []procSocket, endpoint netip.AddrPort, walk endpointHolders) bool {
	attributed := map[uint64]bool{}
	for _, socket := range append(walk.holders[:len(walk.holders):len(walk.holders)], walk.sibling...) {
		attributed[socket.handle] = true
	}
	target := endpoint.Addr().Unmap()
	sibling := siblingLoopback(target)
	for _, socket := range table {
		if !socket.Listening || socket.Local.Port() != endpoint.Port() {
			continue
		}
		addr := socket.Local.Addr().Unmap()
		if (addr == target || addr == sibling || addr.IsUnspecified()) && !attributed[socket.Inode] {
			return false
		}
	}
	return true
}
