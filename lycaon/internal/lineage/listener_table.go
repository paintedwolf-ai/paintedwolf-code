package lineage

import (
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
	IPv6Only bool
	Holders  []ProcessIdentity
}

// ListenerTable is one read of the host's listening sockets. Complete is what
// makes absence a fact: when false the kernel withheld rows, Listeners holds only
// what this user's processes showed directly, and a port not listed may be held.
type ListenerTable struct {
	Listeners []Listener
	Complete  bool
}

// Listeners reads the host's listening sockets from the kernel. Platforms
// without a native socket table return ErrUnsupported.
func Listeners(query ListenerQuery) (ListenerTable, error) { return listeners(query) }

// kernelListener is one listening socket from a host-wide kernel table, keyed
// by the handle the per-process observation reports for the same socket.
type kernelListener struct {
	Protocol Protocol
	Local    netip.AddrPort
	UID      uint32
	IPv6Only bool
	Handle   uint64
}

// socketTableRead is one read of a host-wide table that may hide rows without
// reporting an error: Records holds the handle of every row delivered, Declared
// the count the kernel states for the whole table. Declared alone cannot prove
// withholding, because it also counts closed sockets awaiting collection.
type socketTableRead struct {
	Protocol  Protocol
	Listening []kernelListener
	Records   map[uint64]bool
	Declared  int
}

// reconcileListeners joins table reads with a walk of every readable process.
// A read is withheld when the walk holds a listener it lacks, or when it falls
// short of its declared count and shows no socket of any other process. Listeners
// only the walk saw are still reported, since this user's processes are readable
// whatever the table shows.
func reconcileListeners(reads []socketTableRead, query ListenerQuery, self int, pids []int, observe func(pid int) ([]SocketObservation, error)) ListenerTable {
	walked := make(map[int][]SocketObservation, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if sockets, err := observe(pid); err == nil {
			walked[pid] = sockets
		}
	}
	complete := true
	var rows []kernelListener
	for _, read := range reads {
		listed := make(map[uint64]bool, len(read.Listening))
		for _, row := range read.Listening {
			listed[row.Handle] = true
			if query.wants(row.Protocol, row.Local) {
				rows = append(rows, row)
			}
		}
		showsOther := false
		for _, pid := range pids {
			for _, socket := range walked[pid] {
				if socket.Protocol != read.Protocol {
					continue
				}
				showsOther = showsOther || (pid != self && read.Records[socket.handle])
				if !socket.Listening || listed[socket.handle] {
					continue
				}
				complete = false
				listed[socket.handle] = true
				if query.wants(socket.Protocol, socket.Local) {
					rows = append(rows, kernelListener{Protocol: socket.Protocol, Local: socket.Local, UID: socket.uid, Handle: socket.handle})
				}
			}
		}
		if read.Declared > len(read.Records) && !showsOther {
			complete = false
		}
	}
	return ListenerTable{
		Listeners: resolveListeners(rows, pids, func(pid int) ([]SocketObservation, error) { return walked[pid], nil }),
		Complete:  complete,
	}
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
	ipv6Only := map[uint64]bool{}
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
			if wanted[socket.handle] {
				if socket.IPv6Only {
					ipv6Only[socket.handle] = true
				}
				if !held[socket.handle] {
					held[socket.handle] = true
					holders[socket.handle] = append(holders[socket.handle], socket.Holder)
				}
			}
		}
	}
	out := make([]Listener, 0, len(rows))
	for _, row := range rows {
		out = append(out, Listener{
			Protocol: row.Protocol,
			Addr:     row.Local,
			UID:      row.UID,
			IPv6Only: row.IPv6Only || ipv6Only[row.Handle],
			Holders:  holders[row.Handle],
		})
	}
	return out
}
