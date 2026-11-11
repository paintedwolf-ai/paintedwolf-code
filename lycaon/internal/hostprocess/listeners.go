package hostprocess

import (
	"cmp"
	"context"
	"net/netip"
	"slices"

	"github.com/lycaon/lycaon/internal/lineage"
)

// ListeningSocket is a listening TCP socket or bound, unconnected UDP socket
// and the user the kernel records as its owner.
type ListeningSocket struct {
	Protocol lineage.Protocol `json:"protocol"`
	Address  netip.Addr       `json:"address"`
	Port     int              `json:"port"`
	UID      uint32           `json:"uid"`
}

// PortHolder pairs a listening socket with one process holding it. Process is
// nil when the holder could not be read: the port is held, the PID unknown.
type PortHolder struct {
	Socket  ListeningSocket
	Process *Process
}

// PortHolderTable is one read of the queried port holders. Complete is false
// when the host withheld sockets, so a port with no row may still be held.
type PortHolderTable struct {
	Holders  []PortHolder
	Complete bool
}

// PortHolders reads the queried host listeners and the processes holding them,
// ordered by port, protocol, address and PID. Only the session's own holders
// carry a reference. Callers decide what the caller's approval lets them show.
func (s *Service) PortHolders(ctx context.Context, session string, query lineage.ListenerQuery) (PortHolderTable, error) {
	table, err := s.listeners(query)
	if err != nil {
		return PortHolderTable{}, err
	}
	owned, err := s.members(session)
	if err != nil {
		return PortHolderTable{}, err
	}
	var out []PortHolder
	for _, listener := range table.Listeners {
		socket := ListeningSocket{Protocol: listener.Protocol, Address: listener.Addr.Addr().Unmap(), Port: int(listener.Addr.Port()), UID: listener.UID}
		known := false
		for _, holder := range listener.Holders {
			if err := ctx.Err(); err != nil {
				return PortHolderTable{}, err
			}
			// A holder that exited or whose PID was reused since the socket
			// observation is unknown, never the process now at that PID.
			process, err := inspect(holder.PID)
			if err != nil || holder.Started < 0 || process.StartOrder != uint64(holder.Started) {
				continue
			}
			if _, process.Owned = owned[holder.PID]; process.Owned {
				if process.Reference, err = s.encode(session, process); err != nil {
					return PortHolderTable{}, err
				}
			}
			known = true
			out = append(out, PortHolder{Socket: socket, Process: &process})
		}
		if !known {
			out = append(out, PortHolder{Socket: socket})
		}
	}
	slices.SortStableFunc(out, func(a, b PortHolder) int {
		return cmp.Or(cmp.Compare(a.Socket.Port, b.Socket.Port), cmp.Compare(a.Socket.Protocol, b.Socket.Protocol),
			a.Socket.Address.Compare(b.Socket.Address), cmp.Compare(holderPID(a), holderPID(b)))
	})
	return PortHolderTable{Holders: out, Complete: table.Complete}, nil
}

func holderPID(h PortHolder) int {
	if h.Process == nil {
		return 0
	}
	return h.Process.PID
}

// ListeningSnapshot is a PID-ordered page of processes holding the queried
// listeners. Unattributed lists sockets whose holder could not be read; it
// rides on the first page only. TableComplete is false when the host withheld
// sockets, so a port with no entry may still be held.
type ListeningSnapshot struct {
	Snapshot
	Unattributed  []ListeningSocket
	TableComplete bool
}

// Listening requires prior approval. It pages the queried port holders the way
// List pages processes, each process carrying the sockets it holds.
func (s *Service) Listening(ctx context.Context, session string, query lineage.ListenerQuery, pid, after, limit int) (ListeningSnapshot, error) {
	table, err := s.PortHolders(ctx, session, query)
	if err != nil {
		return ListeningSnapshot{}, err
	}
	out := ListeningSnapshot{TableComplete: table.Complete}
	byPID := map[int]*Process{}
	for _, holder := range table.Holders {
		if holder.Process == nil {
			if after == 0 && pid == 0 {
				out.Unattributed = append(out.Unattributed, holder.Socket)
			}
			continue
		}
		candidate := holder.Process.PID
		if candidate <= after || (pid > 0 && candidate != pid) {
			continue
		}
		process := byPID[candidate]
		if process == nil {
			process = holder.Process
			byPID[candidate] = process
		}
		process.Listeners = append(process.Listeners, holder.Socket)
	}
	pids := make([]int, 0, len(byPID))
	for candidate := range byPID {
		pids = append(pids, candidate)
	}
	slices.Sort(pids)
	if len(pids) > limit {
		pids = pids[:limit]
		out.NextAfterPID = pids[limit-1]
	}
	for _, candidate := range pids {
		out.Processes = append(out.Processes, *byPID[candidate])
	}
	return out, nil
}
