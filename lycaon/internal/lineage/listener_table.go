package lineage

import (
	"fmt"
	"net/netip"
	"slices"
)

// Protocol is a transport whose bound sockets the kernel tables report.
type Protocol string

const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

// ListenerQuery narrows the host table before holders are resolved. No
// protocols means TCP and UDP; port 0 means every port.
type ListenerQuery struct {
	Protocols []Protocol
	Port      uint16
}

func (q ListenerQuery) protocols() []Protocol {
	if len(q.Protocols) == 0 {
		return []Protocol{ProtocolTCP, ProtocolUDP}
	}
	return q.Protocols
}

func (q ListenerQuery) wants(protocol Protocol, local netip.AddrPort) bool {
	return slices.Contains(q.protocols(), protocol) && (q.Port == 0 || local.Port() == q.Port)
}

// Listener is one listening TCP socket or bound, unconnected UDP socket, the
// user the kernel records as its owner, and the process incarnations holding
// it. Holders come from the same native per-process observation SocketsOf
// uses. A holder the host cannot inspect is absent, so an empty list means
// unknown, never unheld.
type Listener struct {
	Protocol Protocol
	Addr     netip.AddrPort
	UID      uint32
	Holders  []ProcessIdentity
}

// Listeners reads the host's listening sockets from the kernel. Platforms
// without a native socket table return ErrUnsupported.
func Listeners(query ListenerQuery) ([]Listener, error) { return listeners(query) }

// CompleteListeners is Listeners for a caller that reasons from what the table
// omits. A view the kernel narrowed returns ErrObservationIncomplete, because an
// absent row there says nothing about whether another process holds the port.
func CompleteListeners(query ListenerQuery) ([]Listener, error) {
	complete, err := listenerViewComplete(query)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrObservationIncomplete, err)
	}
	if !complete {
		return nil, fmt.Errorf("%w: the kernel socket table is narrowed for this process", ErrObservationIncomplete)
	}
	return listeners(query)
}

// kernelListener is one listening socket from a host-wide kernel table, keyed
// by the handle the per-process observation reports for the same socket.
type kernelListener struct {
	Protocol Protocol
	Local    netip.AddrPort
	UID      uint32
	Handle   uint64
}

// tableRow is any socket from a host-wide table, listening or not.
type tableRow struct {
	kernelListener
	Listening bool
}

// tableViewComplete decides whether a table shows the whole host. The kernel can
// return only the caller's own sockets with success while its header still counts
// every socket, so fewer rows than reported are complete only with a row that is
// provably someone else's: another user's, or held by another process.
func tableViewComplete(reported uint64, rows []tableRow, uid uint32, heldElsewhere func(map[uint64]bool) bool) bool {
	if reported <= uint64(len(rows)) {
		return true
	}
	handles := make(map[uint64]bool, len(rows))
	for _, row := range rows {
		if row.UID != uid {
			return true
		}
		handles[row.Handle] = true
	}
	return len(handles) > 0 && heldElsewhere(handles)
}

// resolveListeners attaches to each row every process incarnation whose
// observed sockets include its handle. A process the observation cannot read
// contributes nothing, so its sockets keep an empty holder list rather than
// disappearing.
func resolveListeners(rows []kernelListener, pids []int, observe func(pid int) ([]SocketObservation, error)) []Listener {
	wanted := make(map[uint64]bool, len(rows))
	for _, row := range rows {
		wanted[row.Handle] = true
	}
	holders := map[uint64][]ProcessIdentity{}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		sockets, err := observe(pid)
		if err != nil {
			continue
		}
		held := map[uint64]bool{}
		for _, socket := range sockets {
			if wanted[socket.handle] && !held[socket.handle] {
				held[socket.handle] = true
				holders[socket.handle] = append(holders[socket.handle], socket.Holder)
			}
		}
	}
	out := make([]Listener, 0, len(rows))
	for _, row := range rows {
		out = append(out, Listener{Protocol: row.Protocol, Addr: row.Local, UID: row.UID, Holders: holders[row.Handle]})
	}
	return out
}
